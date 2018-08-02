package send

import (
    "github.com/shidec/smtpd/log"
    "gopkg.in/gomail.v2"
)

var host = "mail.shidec-games.com"
var port = 2525
var username = "root"
var password = "Akuanaksehat22"

func SendMail(from string, to, cc []string, subject, content string, attachments []string) error {
    log.LogTrace("Send email")
    m := gomail.NewMessage()
    m.SetHeader("From", from)
    m.SetHeader("To", to...)
    if len(cc) > 0 {
        m.SetHeader("Cc", cc...)
    }
    m.SetHeader("Subject", subject)
    m.SetBody("text/html", content)

    for _, r := range attachments {
        m.Attach(r)    
    }    

    d := gomail.NewDialer(host, port, username, password)
    err := d.DialAndSend(m)

    return err
}