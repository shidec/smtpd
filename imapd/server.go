/*
Benchmarking:
http://www.jrh.org/smtp/index.html
Test 500 clients:
$ time go-smtp-source -c -l 1000 -t test@localhost -s 500 -m 5000 localhost:25000
*/

package imapd

import (
	"encoding/base64"
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/data"
	"github.com/shidec/smtpd/log"
)

type State int

const (
	STATE_UNAUTHORIZED State = 1
	STATE_AUTHORIZED State = 2
	STATE_LOGGIN_IN State = 3
)

var commands = map[string]bool{
	"CAPABILITY":     true,
	"LOGIN":     true,
	"AUTHENTICATE":     true,
	"LIST":     true,
	"LSUB":     true,
	"LOGOUT":     true,
	"NOOP":     true,
	"CREATE":     true,
	"CLOSE":     true,
	"EXPUNGE":     true,
	"SELECT":     true,
	"EXAMINE":     true,
	"STATUS":     true,
	"UID":     true,
	"QUIT":     true,
	"APPEND":     true,
}

// Real server code starts here
type Server struct {
	Store           *data.DataStore
	domain          string
	maxRecips       int
	maxIdleSeconds  int
	maxMessageBytes int
	storeMessages   bool
	listener        net.Listener
	shutdown        bool
	waitgroup       *sync.WaitGroup
	timeout         time.Duration
	maxClients      int
	TLSConfig       *tls.Config
	ForceTLS        bool
	Debug           bool
	DebugPath       string
	sem             chan int // currently active clients
}

type Client struct {
	server     *Server
	state      State
	user       *data.User
	argId	   string		
	helo       string
	from       string
	recipients []string
	response   string
	remoteHost string
	sendError  error
	data       string
	subject    string
	hash       string
	time       int64
	tls_on     bool
	conn       net.Conn
	bufin      *bufio.Reader
	bufout     *bufio.Writer
	kill_time  int64
	errors     int
	id         int64
	tlsConn    *tls.Conn
	trusted    bool
}

// Init a new Client object
func NewImapServer(cfg config.ImapConfig, ds *data.DataStore) *Server {

	// sem is an active clients channel used for counting clients
	maxClients := make(chan int, cfg.MaxClients)

	return &Server{
		Store:           ds,
		domain:          cfg.Domain,
		maxIdleSeconds:  cfg.MaxIdleSeconds,
		maxMessageBytes: cfg.MaxMessageBytes,
		storeMessages:   cfg.StoreMessages,
		waitgroup:       new(sync.WaitGroup),
		Debug:           cfg.Debug,
		DebugPath:       cfg.DebugPath,
		sem:             maxClients,
	}
}

// Main listener loop
func (s *Server) Start() {
	cfg := config.GetImapConfig()

	log.LogTrace("Loading the certificate: %s", cfg.PubKey)
	cert, err := tls.LoadX509KeyPair(cfg.PubKey, cfg.PrvKey)

	if err != nil {
		log.LogError("There was a problem with loading the certificate: %s", err)
	} else {
		s.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.VerifyClientCertIfGiven,
			ServerName:   cfg.Domain,
		}
		//s.TLSConfig  .Rand = rand.Reader
	}

	defer s.Stop()
	addr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%v:%v", cfg.Ip4address, cfg.Ip4port))
	if err != nil {
		log.LogError("Failed to build tcp4 address: %v", err)
		// TODO More graceful early-shutdown procedure
		//panic(err)
		s.Stop()
		return
	}

	// Start listening for IMAP connections
	log.LogInfo("IMAP listening on TCP4 %v", addr)
	s.listener, err = net.ListenTCP("tcp4", addr)
	if err != nil {
		log.LogError("IMAP failed to start tcp4 listener: %v", err)
		// TODO More graceful early-shutdown procedure
		//panic(err)
		s.Stop()
		return
	}

	//Connect database
	s.Store.StorageConnect()

	var tempDelay time.Duration
	var clientId int64

	// Handle incoming connections
	for clientId = 1; ; clientId++ {
		if conn, err := s.listener.Accept(); err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
				// Temporary error, sleep for a bit and try again
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				log.LogError("IMAP accept error: %v; retrying in %v", err, tempDelay)
				time.Sleep(tempDelay)
				continue
			} else {
				if s.shutdown {
					log.LogTrace("IMAP listener shutting down on request")
					return
				}
				// TODO Implement a max error counter before shutdown?
				// or maybe attempt to restart imapd
				panic(err)
			}
		} else {
			tempDelay = 0
			s.waitgroup.Add(1)
			log.LogInfo("There are now %s serving goroutines", strconv.Itoa(runtime.NumGoroutine()))
			host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

			s.sem <- 1 // Wait for active queue to drain.
			go s.handleClient(&Client{
				state:      STATE_UNAUTHORIZED,
				server:     s,
				conn:       conn,
				remoteHost: host,
				time:       time.Now().Unix(),
				bufin:      bufio.NewReader(conn),
				bufout:     bufio.NewWriter(conn),
				id:         clientId,
			})
		}
	}
}

// Stop requests the IMAP server closes it's listener
func (s *Server) Stop() {
	log.LogTrace("IMAP shutdown requested, connections will be drained")
	s.shutdown = true
	s.listener.Close()
}

// Drain causes the caller to block until all active IMAP sessions have finished
func (s *Server) Drain() {
	s.waitgroup.Wait()
	log.LogTrace("IMAP connections drained")
}

func (s *Server) closeClient(c *Client) {
	c.bufout.Flush()
	time.Sleep(200 * time.Millisecond)
	c.conn.Close()
	<-s.sem // Done; enable next client to run.
}

func (s *Server) killClient(c *Client) {
	c.kill_time = time.Now().Unix()
}

func (s *Server) handleClient(c *Client) {
	log.LogInfo("IMAP Connection from %v, starting session <%v>", c.conn.RemoteAddr(), c.id)

	defer func() {
		s.closeClient(c)
		s.waitgroup.Done()
	}()

	c.greet()

	// This is our command reading loop
	for i := 0; i < 100; i++ {

		if c.state == STATE_LOGGIN_IN {
			// Special case, does not use IMAP command format
			line, _ := c.readLine()
			c.processAuth(line)
			continue
		}

		line, err := c.readLine()
		if err == nil {
			if hdr, cmd, arg, ok := c.parseCmd(line); ok {
				c.handle(hdr, cmd, arg, line)
			}
		} else {
			// readLine() returned an error
			if err == io.EOF {
				c.logWarn("Got EOF while in state %v", c.state)
				break
			}
			// not an EOF
			c.logWarn("Connection error: %v", err)
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				c.Write(c.argId + " NO Idle timeout, bye bye")
				break
			}

			c.Write(c.argId + " Connection error, sorry")
			break
		}

		if c.kill_time > 1 || c.errors > 3 {
			return
		}
	}

	c.logInfo("Closing connection")
}

// Commands are dispatched to the appropriate handler functions.
func (c *Client) handle(hdr string, cmd string, arg string, line string) {
	c.argId = hdr
	c.logTrace("In state %d, got command '%s', args '%s'", c.state, cmd, arg)

	if cmd == "" || !commands[cmd] {
		c.Write(fmt.Sprintf("%s Syntax error, %v command unrecognized", c.argId, cmd))
		c.logWarn("Unrecognized command: %v", cmd)
	}

	switch cmd {
	case "LOGIN":
		c.loginHandler(cmd, arg)
		//return
	case "CAPABILITY":
		c.capabilityHandler(cmd, arg)
		//return
	case "NOOP":
		c.Write(c.argId + " OK I have sucessfully done nothing")
		//return
	case "LIST":
		// Reset session
		c.listHandler(cmd, arg)
		//return
	case "LSUB":
		c.lsubHandler(cmd, arg)
	case "SELECT":
		c.selectHandler(cmd, arg)	
	case "CREATE":
		c.createHandler(cmd, arg)		
		//return
	case "LOGOUT":
		c.Write("* BYE IMAP4rev1 server logging out\r\n")
		c.state = STATE_UNAUTHORIZED
		c.user = nil
		c.Write(c.argId + " OK LOGOUT completed\r\n")
		c.server.killClient(c)
		//return
	case "CLOSE":
		c.state = STATE_UNAUTHORIZED
		c.Write(c.argId + " OK CLOSE completed\r\n")
		//return
	case "AUTHENTICATE":
		c.authHandler(cmd, arg)
	case "UID":
		c.uidHandler(cmd, arg)	
	case "STATUS":
		c.statusHandler(cmd, arg)		
	case "STARTTLS":
		c.tlsHandler()
		//return
	default:
		c.errors++
		if c.errors > 3 {
			c.Write(c.argId +  " NO Too many unrecognized commands")
			c.server.killClient(c)
		}
	}
}

func (c *Client) loginHandler(cmd string, arg string) {
	arg = strings.Replace(arg, "\"", "", -1)
	sp0 := strings.Index(arg, "@")
	sp1 := strings.Index(arg, " ")
	var username string
	if sp0 > 0 {
		username = strings.Trim(arg[0:sp0], " ")
	}else{
		username = strings.Trim(arg[0:sp1], " ")
	}
	
	passwd := strings.Trim(arg[(sp1 + 1):], " ")
	var err error
	c.user, err = c.server.Store.Login(username, passwd)
	if c.user == nil && err != nil {
		c.state = STATE_UNAUTHORIZED
		c.Write(c.argId + " NO Incorrect username/password")
	}else{
		c.state = STATE_AUTHORIZED
		c.Write(c.argId + " OK Authenticated")
	}
}

func (c *Client) capabilityHandler(cmd string, arg string) {
	c.Write("* CAPABILITY IMAP4rev1 AUTH=PLAIN")
	c.Write(c.argId + " OK CAPABILITY completed")
}

func (c *Client) createHandler(cmd string, arg string) {
	if c.state != STATE_AUTHORIZED {
		c.Write(c.argId + " NO not authenticated")
		return
	}

	c.Write(c.argId + " OK create completed")
}

func (c *Client) processAuth(line string) {
	loginRE := regexp.MustCompile("(?:[A-z0-9]+)?\x00([A-z0-9]+)\x00([A-z0-9]+)")

	data, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		c.state = STATE_UNAUTHORIZED
		//c.Write("* BAD Invalid auth details")
		c.Write(c.argId + " NO Invalid auth details")
		return
	}
	match := loginRE.FindSubmatch(data)
	if len(match) != 3 {
		c.state = STATE_UNAUTHORIZED
		c.Write(c.argId + " NO Incorrect username/password")
		return
	}

	c.user, err = c.server.Store.Login(string(match[1]), string(match[2]))
	if err != nil {
		c.state = STATE_UNAUTHORIZED
		c.Write(c.argId + " NO Incorrect username/password")
		return
	}

	c.state = STATE_AUTHORIZED
	c.Write(c.argId + " OK Authenticated")
}

func (c *Client) listHandler(cmd string, arg string) {
	if c.state != STATE_AUTHORIZED {
		c.Write(c.argId + " NO not authenticated")
		return
	}

	if arg == "" {
		// Blank selector means request directory separator
		c.Write("* LIST (\\Noselect) \"/\" \"\"")
	} else if arg == "*" {
		c.Write("* LIST (\\HasNoChildren) \"/\" \"INBOX\"")
	}

	c.Write(c.argId + " OK LIST completed")
}

func (c *Client) lsubHandler(cmd string, arg string) {
	if c.state != STATE_AUTHORIZED {
		c.Write(c.argId + " NO not authenticated")
		return
	}

	c.Write("* LIST () \"/\" \"INBOX\"")
	c.Write(c.argId + " OK LIST completed")
}

func (c *Client) selectHandler(cmd string, arg string) {
	if c.state != STATE_AUTHORIZED {
		c.Write(c.argId + " NO not authenticated")
		return
	}

	c.Write("* " + strconv.Itoa(c.server.Store.Total(c.user.Username)) + " EXISTS")
	c.Write("* " + strconv.Itoa(c.server.Store.Recent(c.user.Username)) + " RECENT")
	c.Write("* OK [UNSEEN " + strconv.Itoa(c.server.Store.Unread(c.user.Username)) + "]")
	c.Write("* OK [UIDNEXT " + strconv.Itoa(c.server.Store.NextId(c.user.Username)) + "]")
	c.Write("* OK [UIDVALIDITY 250]")
	c.Write("* FLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft)")
	c.Write(c.argId + " OK [READ-WRITE] SELECT completed")
}	

func (c *Client) authHandler(cmd string, arg string) {	
	switch arg {
	case "PLAIN":
		c.logInfo("Got PLAIN authentication")
		c.state = STATE_LOGGIN_IN
		c.Write("+")
	default:
		c.logTrace("Unsupported authentication mechanism %v", arg)
		c.Write(c.argId + " BAD Unsupported authentication mechanism")
	}
}

func (c *Client) uidHandler(cmd string, arg string) {
	if c.state != STATE_AUTHORIZED {
		c.Write(c.argId + " NO not authenticated")
		return
	}

	// Fetch the messages
	re := regexp.MustCompile("(?i:FETCH) ([\\d\\:\\*\\,]+) \\(([A-z0-9\\s\\(\\)\\[\\]\\.-]+)\\)")
	match := re.FindSubmatch([]byte(arg))

	seqSet, err := data.InterpretSequenceSet(string(match[1]))
	if err != nil {
		c.Write(c.argId + " NO "+err.Error())
		return
	}

	searchByUID := strings.ToUpper(arg) == "UID "
	//searchByUID := cmd == "UID"

	var msgs []data.Message
	if searchByUID {
		msgs = c.server.Store.MessageSetByUID(c.user.Username, seqSet)
	} else {
		msgs = c.server.Store.MessageSetBySequenceNumber(c.user.Username, seqSet)
	}	
	
	fetchParamString := string(match[2])
	if searchByUID && !strings.Contains(fetchParamString, "UID") {
		fetchParamString += " UID"
	}

	for _, msg := range msgs {
		fetchParams, err := fetch(fetchParamString, msg)
		if err != nil {
			if err == ErrUnrecognisedParameter {
				c.Write(c.argId + " BAD Unrecognised Parameter")
				return
			}

			c.Write(c.argId + " BAD")
			return
		}

		c.server.Store.RemoveRecent(msg)

		fullReply := fmt.Sprintf("* %d FETCH (%s)", msg.Sequence, fetchParams)

		c.Write(fullReply)
	}

	if searchByUID {
		c.Write(c.argId + " OK UID FETCH Completed")
	} else {
		c.Write(c.argId + " OK UID FETCH Completed")
	}
}

func (c *Client) statusHandler(cmd string, arg string) {
	if c.state != STATE_AUTHORIZED {
		c.Write(c.argId + " NO not authenticated")
		return
	}

	var flags []string
	if strings.Contains(arg, "UIDNEXT") {
		
		flags = append(flags, fmt.Sprintf("UIDNEXT %d", c.server.Store.NextId(c.user.Username)))
	}

	if strings.Contains(arg, "MESSAGES") {
		flags = append(flags, fmt.Sprintf("MESSAGES %d", c.server.Store.Total(c.user.Username)))
	}

	if strings.Contains(arg, "RECENT") {
		flags = append(flags, fmt.Sprintf("RECENT %d", c.server.Store.Recent(c.user.Username)))
	}

	if strings.Contains(arg, "UNSEEN") {
		flags = append(flags, fmt.Sprintf("UNSEEN %d", c.server.Store.UnreadCount(c.user.Username)))
	}

	flagList := strings.Join(flags, " ")


	c.Write(fmt.Sprintf("* STATUS %s (%s)", "INBOX", flagList))
	c.Write(c.argId + " OK STATUS Completed")
}

func (c *Client) tlsHandler() {
	if c.tls_on {
		c.Write(c.argId + " NO Already running in TLS")
		return
	}

	if c.server.TLSConfig == nil {
		c.Write(c.argId + " NO TLS not supported")
		return
	}

	log.LogTrace("Ready to start TLS")
	c.Write(c.argId + " OK Ready to start TLS")

	// upgrade to TLS
	var tlsConn *tls.Conn
	tlsConn = tls.Server(c.conn, c.server.TLSConfig)
	err := tlsConn.Handshake() // not necessary to call here, but might as well

	if err == nil {
		//c.conn   = net.Conn(tlsConn)
		c.conn = tlsConn
		c.bufin = bufio.NewReader(c.conn)
		c.bufout = bufio.NewWriter(c.conn)
		c.tls_on = true

		// Reset envelope as a new EHLO/HELO is required after STARTTLS
		c.reset()

		// Reset deadlines on the underlying connection before I replace it
		// with a TLS connection
		c.conn.SetDeadline(time.Time{})
		c.flush()
	} else {
		c.logWarn("Could not TLS handshake:%v", err)
		c.Write(c.argId + " NO Handshake error")
	}

	c.state = 1
}

func (c *Client) greet() {
	c.Write("* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN] ImapServer ready.")
	c.state = STATE_UNAUTHORIZED
}

func (c *Client) flush() {
	c.conn.SetWriteDeadline(c.nextDeadline())
	c.bufout.Flush()
	c.conn.SetReadDeadline(c.nextDeadline())
}

// Calculate the next read or write deadline based on maxIdleSeconds
func (c *Client) nextDeadline() time.Time {
	return time.Now().Add(time.Duration(c.server.maxIdleSeconds) * time.Second)
}

func (c *Client) Write(text string) {
	c.conn.SetDeadline(c.nextDeadline())
	c.logTrace(">> Sent %d bytes: %s >>", len(text), text)
	c.conn.Write([]byte(text + "\r\n"))
	c.bufout.Flush()
	return
}

// readByteLine reads a line of input into the provided buffer. Does
// not reset the Buffer - please do so prior to calling.
func (c *Client) readByteLine(buf *bytes.Buffer) error {
	if err := c.conn.SetReadDeadline(c.nextDeadline()); err != nil {
		return err
	}
	for {
		line, err := c.bufin.ReadBytes('\r')
		if err != nil {
			return err
		}
		buf.Write(line)
		// Read the next byte looking for '\n'
		c, err := c.bufin.ReadByte()
		if err != nil {
			return err
		}
		buf.WriteByte(c)
		if c == '\n' {
			// We've reached the end of the line, return
			return nil
		}
		// Else, keep looking
	}
	// Should be unreachable
}

// Reads a line of input
func (c *Client) readLine() (line string, err error) {
	if err = c.conn.SetReadDeadline(c.nextDeadline()); err != nil {
		return "", err
	}

	line, err = c.bufin.ReadString('\n')
	if err != nil {
		return "", err
	}
	c.logTrace("<< %v <<", strings.TrimRight(line, "\r\n"))
	return line, nil
}

func (c *Client) parseCmd(line string) (hdr string, cmd string, arg string, ok bool) {
	line = strings.TrimRight(line, "\r\n")
	sp := strings.Index(line, " ");
	if sp < 0 {
		return "", "", "", false
	}
	
	shdr := line[0:sp]
	scmd := line[(sp + 1):]
	sp2 := strings.Index(scmd, " ")
	if(sp2 >= 0){
		return shdr, strings.ToUpper(scmd[0:sp2]), scmd[(sp2 + 1):], true
	}else{
		return shdr, strings.ToUpper(scmd), "", true
	}
	
}

// parseArgs takes the arguments proceeding a command and files them
// into a map[string]string after uppercasing each key.  Sample arg
// string:
//		" BODY=8BITMIME SIZE=1024"
// The leading space is mandatory.
func (c *Client) parseArgs(arg string) (args map[string]string, ok bool) {
	args = make(map[string]string)
	re := regexp.MustCompile(" (\\w+)=(\\w+)")
	pm := re.FindAllStringSubmatch(arg, -1)
	if pm == nil {
		c.logWarn("Failed to parse arg string: %q")
		return nil, false
	}
	for _, m := range pm {
		args[strings.ToUpper(m[1])] = m[2]
	}
	c.logTrace("EIMAP params: %v", args)
	return args, true
}

func (c *Client) reset() {
	c.state = 1
	c.from = ""
	c.helo = ""
	c.recipients = nil
}

// Session specific logging methods
func (c *Client) logTrace(msg string, args ...interface{}) {
	log.LogTrace("IMAP[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}

func (c *Client) logInfo(msg string, args ...interface{}) {
	log.LogInfo("IMAP[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}

func (c *Client) logWarn(msg string, args ...interface{}) {
	// Update metrics
	//expWarnsTotal.Add(1)
	log.LogWarn("IMAP[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}

func (c *Client) logError(msg string, args ...interface{}) {
	// Update metrics
	//expErrorsTotal.Add(1)
	log.LogError("IMAP[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}