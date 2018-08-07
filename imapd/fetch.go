package imapd

import (
	"errors"
	"fmt"
	"net/textproto"
	"regexp"
	"strings"
	"strconv"

	"github.com/shidec/smtpd/data"
	"github.com/shidec/smtpd/log"
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
	registerFetchParam("BODYSTRUCTURE", fetchBodyStructure)

}

// FetchBodyStructure computes a message's body structure from its content.
//func fetchBodyStructure(e *message.Entity, extended bool) (*imap.BodyStructure, error) {
func fetchBodyStructure(params string, m data.Message, peekOnly bool) string {	
	if len(m.Attachments) == 0 {
		if len(m.MIME.Parts) == 1 {
			content := strings.Split(m.MIME.Parts[0].ContentType, "/")
			return  fmt.Sprintf("BODYSTRUCTURE (\"%s\" \"%s\" (\"CHARSET\" \"%s\") NIL NIL \"QUOTED-PRINTABLE\" %d %d NIL NIL NIL NIL)",
				strings.ToUpper(content[0]), 
				strings.ToUpper(content[1]), 
				strings.ToUpper(m.MIME.Parts[0].Charset), 
				m.MIME.Parts[0].Size,
				m.MIME.Parts[0].Lines)
		} else {
			content := strings.Split(m.MIME.Parts[0].ContentType, "/")

			result := "BODYSTRUCTURE("
			for _, part := range m.MIME.Parts {
				content = strings.Split(part.ContentType, "/")
				result = result + fmt.Sprintf("(\"%s\" \"%s\" (\"CHARSET\" \"%s\") NIL NIL \"QUOTED-PRINTABLE\" %d %d NIL NIL NIL NIL)",
				strings.ToUpper(content[0]), 
				strings.ToUpper(content[1]), 
				strings.ToUpper(part.Charset), 
				part.Size, 
				part.Lines) 
			}
			return result + fmt.Sprintf("\"ALTERNATIVE\" (\"BOUNDARY\" \"%s\") NIL NIL NIL)", m.MIME.Parts[0].Boundary)
		}
	}else{
		if len(m.MIME.Parts) == 1 {
			content := strings.Split(m.MIME.Parts[0].ContentType, "/")

			result := "BODYSTRUCTURE("
			result = result + fmt.Sprintf("(\"%s\" \"%s\" (\"CHARSET\" \"%s\") NIL NIL \"QUOTED-PRINTABLE\" %d %d NIL NIL NIL NIL)",
				strings.ToUpper(content[0]), 
				strings.ToUpper(content[1]), 
				strings.ToUpper(m.MIME.Parts[0].Charset), 
				m.MIME.Parts[0].Size, 
				m.MIME.Parts[0].Lines)

			for _, att := range m.Attachments {
				content = strings.Split(att.ContentType, "/")
				result = result + fmt.Sprintf("(\"%s\" \"%s\" (\"NAME\" \"%s\") NIL NIL \"%s\" %d NIL (\"attachment\" (\"FILENAME\" \"%s\" \"SIZE\" \"%d\")) NIL NIL)",
					strings.ToUpper(content[0]), 
					strings.ToUpper(content[1]), 
					strings.ToUpper(att.FileName), 
					strings.ToUpper(att.TransferEncoding), 
					att.Size, 
					strings.ToUpper(att.FileName), 
					att.Size) 
			}
			return result + fmt.Sprintf("\"MIXED\" (\"BOUNDARY\" \"%s\") NIL NIL NIL)", m.Attachments[0].Boundary)
		} else {
			var content []string

			result := "BODYSTRUCTURE(("
			for _, part := range m.MIME.Parts {
				content = strings.Split(part.ContentType, "/")
				result = result + fmt.Sprintf("(\"%s\" \"%s\" (\"CHARSET\" \"%s\") NIL NIL \"QUOTED-PRINTABLE\" %d %d NIL NIL NIL NIL)",
				strings.ToUpper(content[0]), 
				strings.ToUpper(content[1]), 
				strings.ToUpper(part.Charset), 
				part.Size, 
				part.Lines) 
			}
			result = result + fmt.Sprintf("\"ALTERNATIVE\" (\"BOUNDARY\" \"%s\") NIL NIL NIL)", m.MIME.Parts[0].Boundary)
			result = result + ")"

			for _, att := range m.Attachments {
				content = strings.Split(att.ContentType, "/")
				result = result + fmt.Sprintf("(\"%s\" \"%s\" (\"NAME\" \"%s\") NIL NIL \"%s\" %d NIL (\"attachment\" (\"FILENAME\" \"%s\" \"SIZE\" \"%d\")) NIL NIL)",
					strings.ToUpper(content[0]), 
					strings.ToUpper(content[1]), 
					strings.ToUpper(att.FileName), 
					strings.ToUpper(att.TransferEncoding), 
					att.Size, 
					strings.ToUpper(att.FileName), 
					att.Size) 
			}
			return result + fmt.Sprintf("\"MIXED\" (\"BOUNDARY\" \"%s\") NIL NIL NIL)", m.Attachments[0].Boundary)
		}
	}

	return ""
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
				log.LogTrace("fetchHeaders:" + key + ":" + strconv.Itoa(len(v)))
				for _ , vv := range v {
					log.LogTrace("fetchHeader:" + vv)
				}
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

/*
func ContentType(h map[string][]string) (t string, params map[string]string, err error) {
	val := data.getMapValue(h, "Content-Type")
	if val == "" {
		return "text/plain", nil, nil
	}

	return parseHeaderWithParams(val[0])
	
}
*/