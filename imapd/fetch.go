package imapd

import (
	"errors"
	"fmt"
	"net/textproto"
	"regexp"
	"strings"
	"github.com/shidec/smtpd/data"
)

var registeredFetchParams []fetchParamDefinition
var peekRE *regexp.Regexp

// ErrUnrecognisedParameter indicates that the parameter requested in a FETCH
// command is unrecognised or not implemented in this IMAP server
var ErrUnrecognisedParameter = errors.New("Unrecognised Parameter")

type fetchParamDefinition struct {
	re      *regexp.Regexp
	handler func(string, data.Message, bool) string
}

// Register all supported fetch parameters
func init() {
	peekRE = regexp.MustCompile("\\.PEEK")
	registeredFetchParams = make([]fetchParamDefinition, 0)
	registerFetchParam("UID", fetchUID)
	registerFetchParam("FLAGS", fetchFlags)
	registerFetchParam("RFC822\\.SIZE", fetchRfcSize)
	registerFetchParam("INTERNALDATE", fetchInternalDate)
	registerFetchParam("BODY(?:\\.PEEK)?\\[HEADER\\]", fetchHeaders)
	registerFetchParam("BODY(?:\\.PEEK)?"+"\\[HEADER\\.FIELDS \\(([A-z\\s-]+)\\)\\]", fetchHeaderSpecificFields)
	registerFetchParam("BODY(?:\\.PEEK)?\\[TEXT\\]", fetchBody)
	registerFetchParam("BODY(?:\\.PEEK)?\\[\\]", fetchFullText)
}





func fetch(params string, m data.Message) (string, error) {
	paramList := data.SplitParams(params)

	// Prepare the list of responses
	responseParams := make([]string, 0, len(paramList))

	for _, param := range paramList {
		paramResponse, err := fetchParam(param, m)
		if err != nil {
			return "", err
		}
		responseParams = append(responseParams, paramResponse)
	}
	return strings.Join(responseParams, " "), nil
}

// Match a single fetch parameter and return the data
func fetchParam(param string, m data.Message) (string, error) {
	peekRE = regexp.MustCompile("\\.PEEK")
	peek := false
	if peekRE.MatchString(param) {
		peek = true
	}
	// Search through the parameter list until a parameter handler is found
	for _, element := range registeredFetchParams {
		if element.re.MatchString(param) {
			return element.handler(param, m, peek), nil
			//strs := element.re.FindStringSubmatch(param)
			//return element.handler(strs, m, peek), nil
		}
	}
	return "", ErrUnrecognisedParameter
}

func registerFetchParam(regex string, handler func(string, data.Message, bool) string) {
	newParam := fetchParamDefinition{
		re:      regexp.MustCompile(regex),
		handler: handler,
	}
	registeredFetchParams = append(registeredFetchParams, newParam)
}

// Fetch the UID of the mail message
func fetchUID(param string, m data.Message, peekOnly bool) string {
	return fmt.Sprintf("UID %d", m.Sequence)
}

func fetchFlags(param string, m data.Message, peekOnly bool) string {
	
	var flags []string
	if m.Unread {
		//UNSEEN
		flags = append(flags, "\\Seen")
	}

	if m.Recent {
		//RECENT
		flags = append(flags, "\\Recent")
	}

	flagList := strings.Join(flags, " ")
	return fmt.Sprintf("FLAGS (%s) UID %d", flagList, m.Sequence)
	
	//return fmt.Sprintf("FLAGS (UNSEEN RECENT)")
}

func fetchRfcSize(param string, m data.Message, peekOnly bool) string {
	return fmt.Sprintf("RFC822.SIZE %d", m.Content.Size)
}

func fetchInternalDate(param string, m data.Message, peekOnly bool) string {
	dateStr := m.Created
	return fmt.Sprintf("INTERNALDATE \"%s\"", dateStr)
}

func fetchHeaders(param string, m data.Message, peekOnly bool) string {
	hdr := fmt.Sprintf("%s\r\n", data.MIMEHeaderToString(m.Content.Headers))
	hdrLen := len(hdr)

	peekStr := ""
	if peekOnly {
		peekStr = ".PEEK"
	}

	return fmt.Sprintf("BODY%s[HEADER] {%d}\r\n%s", peekStr, hdrLen, hdr)
}


func fetchHeaderSpecificFields(param string, m data.Message, peekOnly bool) string {
	if !peekOnly {
		fmt.Printf("TODO: Peek not requested, mark all as non-recent\n")
	}
	fields := strings.Split(param, " ")
	hdrs := m.Content.Headers
	requestedHeaders := make(textproto.MIMEHeader)
	replyFieldList := make([]string, len(fields))
	for i, key := range fields {
		replyFieldList[i] = "\"" + key + "\""
		// If the key exists in the headers, copy it over
		if v, ok := hdrs[key]; ok {
			if len(v) > 0 {
    			requestedHeaders.Add(key, v[0])
    		}
		}
	}
	hdr := data.MIMEHeaderToString(requestedHeaders)
	hdrLen := len(hdr)

	return fmt.Sprintf("BODY[HEADER.FIELDS (%s)] {%d}\r\n%s",
		strings.Join(replyFieldList, " "),
		hdrLen,
		hdr)

}

func fetchBody(param string, m data.Message, peekOnly bool) string {
	body := fmt.Sprintf("%s\r\n", m.Content.Body)
	bodyLen := len(body)

	return fmt.Sprintf("BODY[TEXT] {%d}\r\n%s",
		bodyLen, body)
}

func fetchFullText(param string, m data.Message, peekOnly bool) string {
	mail := fmt.Sprintf("%s\r\n%s\r\n", data.MIMEHeaderToString(m.Content.Headers), m.Content.Body)
	mailLen := len(mail)

	return fmt.Sprintf("BODY[] {%d}\r\n%s",
		mailLen, mail)
}