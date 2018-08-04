package send

import (
    "github.com/shidec/smtpd/log"
    "gopkg.in/gomail.v2"
    "github.com/shidec/smtpd/config"
)

func SendMail(from string, to, cc []string, subject, content string, attachments []string) error {
    log.LogTrace("Send email")
    cfg := config.GetMtaConfig()
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

    d := gomail.NewDialer(cfg.Host, cfg.Port, cfg.Username, cfg.Password)
    err := d.DialAndSend(m)

    return err
}