/*
	This is the smtpd daemon launcher
	./smtpd -config=etc/smtpd.conf -logfile=smtpd.log &
*/
package main

import (
	"flag"
	"fmt"
	golog "log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/data"
	"github.com/shidec/smtpd/log"
	"github.com/shidec/smtpd/smtpd"
	"github.com/shidec/smtpd/imapd"
	"github.com/shidec/smtpd/pop3d"
	"github.com/shidec/smtpd/web"
)

var (
	// Build info, populated during linking by goxc
	VERSION    = "2.1"
	BUILD_DATE = "undefined"

	// Command line flags
	help       = flag.Bool("help", false, "Displays this help")
	pidfile    = flag.String("pidfile", "none", "Write our PID into the specified file")
	logfile    = flag.String("logfile", "stderr", "Write out log into the specified file")
	configfile = flag.String("config", "/etc/smtpd.conf", "Path to the configuration file")

	// startTime is used to calculate uptime of Smtpd
	startTime = time.Now()

	// The file we send log output to, will be nil for stderr or stdout
	logf *os.File

	// Server instances
	smtpServer *smtpd.Server

/*	pop3Server *pop3d.Server*/
)

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(runtime.NumCPU())

	if *help {
		flag.Usage()
		return
	}

	// Load & Parse config
	/*	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}*/

	//err := config.LoadConfig(flag.Arg(0))
	err := config.LoadConfig(*configfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse config: %v\n", err)
		os.Exit(1)
	}

	// Setup signal handler
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGTERM)
	go signalProcessor(sigChan)

	// Configure logging, close std* fds
	level, _ := config.Config.String("logging", "level")
	log.SetLogLevel(level)

	if *logfile != "stderr" {
		// stderr is the go logging default
		if *logfile == "stdout" {
			// set to stdout
			golog.SetOutput(os.Stdout)
		} else {
			err := openLogFile()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v", err)
				os.Exit(1)
			}
			defer closeLogFile()

			// close std* streams
			os.Stdout.Close()
			os.Stderr.Close() // Warning: this will hide panic() output
			os.Stdin.Close()
			os.Stdout = logf
			os.Stderr = logf
		}
	}

	log.LogInfo("Smtpd %v (%v) starting...", VERSION, BUILD_DATE)

	// Write pidfile if requested
	// TODO: Probably supposed to remove pidfile during shutdown
	if *pidfile != "none" {
		pidf, err := os.Create(*pidfile)
		if err != nil {
			log.LogError("Failed to create %v: %v", *pidfile, err)
			os.Exit(1)
		}
		defer pidf.Close()
		fmt.Fprintf(pidf, "%v\n", os.Getpid())
	}

	// Grab our datastore
	ds := data.NewDataStore()

	webConfig := config.GetWebConfig()
	smtpConfig := config.GetSmtpConfig()

	webConfig.Domain = smtpConfig.Domain
	// Start HTTP server
	if webConfig.Available {
		web.Initialize(webConfig, ds)
		go web.Start()
	}

	
	// Startup SMTP server, block until it exits
	if smtpConfig.Available {
		smtpServer = smtpd.NewSmtpServer(smtpConfig, ds)
		go smtpServer.Start()

		//Wait for active connections to finish
		//smtpServer.Drain()
	}

	imapConfig := config.GetImapConfig()
	// Startup IMAP server, block until it exits
	if imapConfig.Available {
		imapServer := imapd.NewImapServer(imapConfig, ds)
		go imapServer.Start()
		// Wait for active connections to finish
		//imapServer.Drain()
	}

	pop3Config := config.GetPop3Config()
	// Startup IMAP server, block until it exits
	if pop3Config.Available {
		pop3Server := pop3d.NewPop3Server(pop3Config, ds)
		pop3Server.Start()
		// Wait for active connections to finish
		pop3Server.Drain()
	}
}

// openLogFile creates or appends to the logfile passed on commandline
func openLogFile() error {
	// use specified log file
	var err error
	logf, err = os.OpenFile(*logfile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		return fmt.Errorf("Failed to create %v: %v\n", *logfile, err)
	}
	golog.SetOutput(logf)
	log.LogTrace("Opened new logfile")
	return nil
}

// closeLogFile closes the current logfile
func closeLogFile() error {
	log.LogTrace("Closing logfile")
	return logf.Close()
}

// signalProcessor is a goroutine that handles OS signals
func signalProcessor(c <-chan os.Signal) {
	for {
		sig := <-c
		switch sig {
		case syscall.SIGHUP:
			// Rotate logs if configured
			if logf != nil {
				log.LogInfo("Recieved SIGHUP, cycling logfile")
				closeLogFile()
				openLogFile()
			} else {
				log.LogInfo("Ignoring SIGHUP, logfile not configured")
			}
		case syscall.SIGTERM:
			// Initiate shutdown
			log.LogInfo("Received SIGTERM, shutting down")
			go timedExit()
			web.Stop()
			if smtpServer != nil {
				smtpServer.Stop()
			} else {
				log.LogError("smtpServer was nil during shutdown")
			}
		}
	}
}

// timedExit is called as a goroutine during shutdown, it will force an exit after 15 seconds
func timedExit() {
	time.Sleep(15 * time.Second)
	log.LogError("Smtpd clean shutdown timed out, forcing exit")
	os.Exit(0)
}

func init() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage of smtpd [options]:")
		flag.PrintDefaults()
	}
}
