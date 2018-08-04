/*
Benchmarking:
http://www.jrh.org/smtp/index.html
Test 500 clients:
$ time go-smtp-source -c -l 1000 -t test@localhost -s 500 -m 5000 localhost:25000
*/

package pop3d

import (
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
	//"gopkg.in/mgo.v2/bson"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/data"
	"github.com/shidec/smtpd/log"
)

type State int

const (
	STATE_UNAUTHORIZED State = 1
	STATE_TRANSACTION State = 2
	STATE_UPDATE State = 3
)


var commands = map[string]bool{
	"USER":     true,
	"PASS":     true,
	"STAT":     true,
	"LIST":     true,
	"UIDL":     true,
	"RETR":     true,
	"DELE":     true,
	"APOP":     true,
	"QUIT":     true,
}

// Real server code starts here
type Server struct {
	Store           *data.DataStore
	cfg             *config.Pop3Config
	domain          string
	maxRecips       int
	maxIdleSeconds  int
	listener        net.Listener
	shutdown        bool
	waitgroup       *sync.WaitGroup
	timeout         time.Duration
	maxClients      int
	TLSConfig       *tls.Config
	ForceTLS        bool
	sem             chan int // currently active clients
}

type Client struct {
	server     *Server
	state      State
	username   string
	user       *data.User
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
func NewPop3Server(cfg config.Pop3Config, ds *data.DataStore) *Server {

	// sem is an active clients channel used for counting clients
	maxClients := make(chan int, cfg.MaxClients)

	return &Server{
		Store:           ds,
		cfg:			 &cfg,
		domain:          cfg.Domain,
		maxIdleSeconds:  cfg.MaxIdleSeconds,
		waitgroup:       new(sync.WaitGroup),
		sem:             maxClients,
	}
}

// Main listener loop
func (s *Server) Start() {

	defer s.Stop()
	addr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%v:%v", s.cfg.Ip4address, s.cfg.Ip4port))
	if err != nil {
		log.LogError("Failed to build tcp4 address: %v", err)
		s.Stop()
		return
	}

	// Start listening for POP3 connections
	log.LogInfo("POP3 listening on TCP4 %v", addr)
	s.listener, err = net.ListenTCP("tcp4", addr)
	if err != nil {
		log.LogError("POP3 failed to start tcp4 listener: %v", err)
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
				log.LogError("POP3 accept error: %v; retrying in %v", err, tempDelay)
				time.Sleep(tempDelay)
				continue
			} else {
				if s.shutdown {
					log.LogTrace("POP3 listener shutting down on request")
					return
				}
				// TODO Implement a max error counter before shutdown?
				// or maybe attempt to restart pop3d
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
				username:   "",
				user:		nil,
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

// Stop requests the POP3 server closes it's listener
func (s *Server) Stop() {
	log.LogTrace("POP3 shutdown requested, connections will be drained")
	s.shutdown = true
	s.listener.Close()
}

// Drain causes the caller to block until all active POP3 sessions have finished
func (s *Server) Drain() {
	s.waitgroup.Wait()
	log.LogTrace("POP3 connections drained")
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
	log.LogInfo("POP3 Connection from %v, starting session <%v>", c.conn.RemoteAddr(), c.id)

	defer func() {
		s.closeClient(c)
		s.waitgroup.Done()
	}()

	c.greet()

	// This is our command reading loop
	for i := 0; i < 100; i++ {

		/*
		if c.state == 99 {
			// Special case, does not use POP3 command format
			//line, _ := c.readLine()
			continue
		}
		*/

		line, err := c.readLine()
		if err == nil {
			if cmd, arg, ok := c.parseCmd(line); ok {
				c.handle(cmd, arg, line)
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
				c.Write("-ERR Disconected for inactivity")
				break
			}

			c.Write("-ERR Connection error, sorry")
			break
		}

		if c.kill_time > 1 || c.errors > 3 {
			return
		}
	}

	c.logInfo("Closing connection")
}

// Commands are dispatched to the appropriate handler functions.
func (c *Client) handle(cmd string, arg string, line string) {
	c.logTrace("In state %d, got command '%s', args '%s'", c.state, cmd, arg)

	// Check against valid POP3 commands
	if cmd == "" {
		c.Write("-ERR Speak up")
		//return
	}

	if cmd != "" && !commands[cmd] {
		c.Write("-ERR " + fmt.Sprintf("Syntax error, %v command unrecognized", cmd))
		c.logWarn("Unrecognized command: %v", cmd)
	}

	switch cmd {
	case "USER":
		c.userHandler(cmd, arg)
	case "PASS":
		c.passHandler(cmd, arg)
	case "STAT":
		// Reset session
		c.statHandler(cmd, arg)
	case "LIST":
		// Reset session
		c.listHandler(cmd, arg)
		//return
	case "UIDL":
		// Reset session
		c.uidlHandler(cmd, arg)
		//return	
	case "RETR":
		c.retrHandler(cmd, arg)
	case "DELE":
		c.deleHandler(cmd, arg)	
	/*	
	case "APOP":
		c.apopHandler(cmd, arg)		
		//return
		*/
	case "QUIT":
		c.Write("+OK server logging out\r\n")
		c.state = STATE_UNAUTHORIZED
		c.user = nil
		c.username = ""
		c.server.killClient(c)
		//return
	default:
		c.errors++
		if c.errors > 3 {
			c.Write("-ERR Too many unrecognized commands")
			c.server.killClient(c)
		}
	}
}

func (c *Client) userHandler(cmd string, arg string) {
	c.username = arg
	c.Write("+OK " + arg)
}

func (c *Client) passHandler(cmd string, arg string) {
	if(c.username == ""){
		c.Write("-ERR NO username given")
		return
	}
	
	var err error
	c.user, err = c.server.Store.Login(c.username, arg)
	if c.user == nil && err != nil {
		c.Write("-ERR Authentication error")
	}else{
		c.state = STATE_TRANSACTION
		c.Write("+OK Logged in")
	}
}

func (c *Client) statHandler(cmd string, arg string) {
	if c.state != STATE_TRANSACTION {
		c.Write("-ERR not authenticated")
		return
	}
	count, total, _ := c.server.Store.Pop3GetStat(c.user.Username)
	c.Write("+OK " + strconv.Itoa(count) + " " + strconv.Itoa(total))
}

func (c *Client) listHandler(cmd string, arg string) {
	if c.state != STATE_TRANSACTION {
		c.Write("-ERR not authenticated")
		return
	}
	msgs, _ := c.server.Store.Pop3GetList(c.user.Username)
 
	c.Write("+OK " + strconv.Itoa(len(msgs)) + " messages")
	for _, msg := range msgs {
		c.Write(strconv.Itoa(msg.Sequence) + " " + strconv.Itoa(msg.Content.Size))		
	}
	c.Write(".")
}

func (c *Client) uidlHandler(cmd string, arg string) {
	if c.state != STATE_TRANSACTION {
		c.Write("-ERR not authenticated")
		return
	}
	msgs, _ := c.server.Store.Pop3GetUidl(c.user.Username)
 
	c.Write("+OK")
	for _, msg := range msgs {
		c.Write(strconv.Itoa(msg.Sequence) + " " + msg.Id)		
	}
	c.Write(".")
}

func (c *Client) retrHandler(cmd string, arg string) {
	if c.state != STATE_TRANSACTION {
		c.Write("-ERR not authenticated")
		return
	}
	
	id, _ := strconv.Atoi(arg)
	msg, _ := c.server.Store.Pop3GetRetr(c.user.Username, id)
 
	c.Write("+OK " + strconv.Itoa(msg.Content.Size) + " octets")
	c.Write(msg.Content.Body)		
	c.Write(".")
}

func (c *Client) deleHandler(cmd string, arg string) {
	if c.state != STATE_TRANSACTION {
		c.Write("-ERR not authenticated")
		return
	}

	id, _ := strconv.Atoi(arg)
	err := c.server.Store.Pop3GetDele(c.user.Username, id)
 	
 	if err != nil {
 		c.Write("-ERR " + err.Error())
 	}
	c.Write("+OK")
}

func (c *Client) reject() {
	c.Write("-ERR Too busy. Try again later.")
	c.server.closeClient(c)
}

func (c *Client) enterState(state State) {
	c.state = state
	c.logTrace("Entering state %v", state)
}

func (c *Client) greet() {
	c.Write("+OK POP3 server ready <" + c.server.cfg.Domain + ">")
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
	/*
	for i := 0; i < len(text)-1; i++ {
		c.logTrace(">> Sent %d bytes: %s >>", len(text[i]), text[i])
		c.conn.Write([]byte(code + "-" + text[i] + "\r\n"))
	}
	c.logTrace(">> Sent %d bytes: %s >>", len(text[len(text)-1]), text[len(text)-1])
	c.conn.Write([]byte(code + " " + text[len(text)-1] + "\r\n"))

	c.bufout.Flush()
	*/
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

func (c *Client) parseCmd(line string) (cmd string, arg string, ok bool) {
	line = strings.TrimRight(line, "\r\n")
	sp := strings.Index(line, " ");
	if sp < 0 {
		return strings.ToUpper(line), "", true
	}	
	scmd := line[0:sp]
	sarg := line[(sp + 1):]
	return strings.ToUpper(scmd), sarg, true	
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
	log.LogTrace("POP3[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}

func (c *Client) logInfo(msg string, args ...interface{}) {
	log.LogInfo("POP3[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}

func (c *Client) logWarn(msg string, args ...interface{}) {
	// Update metrics
	//expWarnsTotal.Add(1)
	log.LogWarn("POP3[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}

func (c *Client) logError(msg string, args ...interface{}) {
	// Update metrics
	//expErrorsTotal.Add(1)
	log.LogError("POP3[%v]<%v> %v", c.remoteHost, c.id, fmt.Sprintf(msg, args...))
}