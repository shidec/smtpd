package send

import (
    "github.com/shidec/smtpd/log"
    "gopkg.in/gomail.v2"
)

var host = "mail.shidec-games.com"
var port = 2525
var username = "root"
var password = "Akuanaksehat22"

func SendMail(from string, to, cc []string, subject, content string, attachments []string) {
    log.LogTrace("Send email:0")

    m := gomail.NewMessage()
    m.SetHeader("From", from)
    m.SetHeader("To", to...)
    m.SetHeader("Cc", cc...)
    m.SetHeader("Subject", subject)
    m.SetBody("text/html", content)

    for _, r := range attachments {
        m.Attach(r)    
    }
    

    d := gomail.NewDialer(host, port, username, password)
    if err := d.DialAndSend(m); err != nil {
        log.LogTrace("Send email:e0:" + err.Error())
        panic(err)
    }    
    log.LogTrace("Send email:1")
}