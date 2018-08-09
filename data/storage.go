package data

import (
	"fmt"
	"io"
	"time"
	"encoding/base64"
    "os"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/log"
	"github.com/shidec/smtpd/send"
	"gopkg.in/mgo.v2/bson"
)

type DataStore struct {
	Config         config.DataStoreConfig
	Domain         string
	Storage        interface{}
	SaveMailChan   chan *config.SMTPMessage
	NotifyMailChan chan interface{}
}

// DefaultDataStore creates a new DataStore object.
func NewDataStore() *DataStore {
	cfg := config.GetDataStoreConfig()

	// Database Writing
	saveMailChan := make(chan *config.SMTPMessage, 256)

	// Websocket Notification
	notifyMailChan := make(chan interface{}, 256)

	return &DataStore{Config: cfg, SaveMailChan: saveMailChan, NotifyMailChan: notifyMailChan}
}

func (ds *DataStore) StorageConnect() {

	if ds.Config.Storage == "mongodb" {
		log.LogInfo("Trying MongoDB storage")
		s := CreateMongoDB(ds.Config)
		if s == nil {
			log.LogInfo("MongoDB storage unavailable")
		} else {
			log.LogInfo("Using MongoDB storage")
			ds.Storage = s
		}

		// start some savemail workers
		for i := 0; i < 3; i++ {
			go ds.SaveMail()
		}
	}
}

func (ds *DataStore) StorageDisconnect() {
	if ds.Config.Storage == "mongodb" {
		ds.Storage.(*MongoDB).Close()
	}
}

func (ds *DataStore) RemoveRecent(m Message) {
	ds.Storage.(*MongoDB).RemoveRecent(m)
}

func (ds *DataStore) SaveMail() {
	log.LogTrace("Running SaveMail Rotuines")
	var err error
	var recon bool

	for {
		mc := <-ds.SaveMailChan
		

		if ds.Config.Storage == "mongodb" {
			msg := ParseSMTPMessage(ds.Storage.(*MongoDB), mc, mc.Domain, ds.Config.MimeParser)

			sfrom := msg.From.Mailbox + "@" + msg.From.Domain
			var ato []string

			for _, path := range msg.To {
				if path.Domain != mc.Domain {
					sto := path.Mailbox + "@" + path.Domain
					ato = append(ato, sto)
					log.LogTrace("Remote address : " + sto)
				}
			}

			if len(ato) > 0 {
				log.LogTrace("Not for local addres, send it")
				
				ats := []string{}
				for _ , at := range msg.Attachments {
					at.SaveToFile()
					ats = append(ats, "/tmp/" + at.FileName)
				}

				err := send.SendMail(sfrom, ato, []string{}, msg.Subject, msg.Content.HtmlBody, ats)
				if err == nil {
					log.LogTrace("Email sent")
				}else{
					log.LogError("Email failed to be sent :" + err.Error())
				}
			}

			mc.Hash, err = ds.Storage.(*MongoDB).Store(msg)

			// if mongo conection is broken, try to reconnect only once
			if err == io.EOF && !recon {
				log.LogWarn("Connection error trying to reconnect")
				ds.Storage = CreateMongoDB(ds.Config)
				recon = true

				//try to save again
				mc.Hash, err = ds.Storage.(*MongoDB).Store(msg)
			}

			if err == nil {
				recon = false
				log.LogTrace("Save Mail Client hash : <%s>", mc.Hash)
				mc.Notify <- 1

				//Notify web socket
				ds.NotifyMailChan <- mc.Hash
			} else {
				mc.Notify <- -1
				log.LogError("Error storing message: %s", err)
			}
		}
	}
}

func (at *Attachment) SaveToFile() error {
	dec, err := base64.StdEncoding.DecodeString(at.Body)
    if err != nil {
        return err
    }

    f, err := os.Create("/tmp/" + at.FileName)
    if err != nil {
        return err
    }
    defer f.Close()

    if _, err := f.Write(dec); err != nil {
        return err
    }

    if err := f.Sync(); err != nil {
        return err
    }

    return nil
}

// Check if host address is in greylist
// h -> hostname client ip
func (ds *DataStore) CheckGreyHost(h string) bool {
	to, err := ds.Storage.(*MongoDB).IsGreyHost(h)
	if err != nil {
		return false
	}

	return to > 0
}

// Check if host address is in greylist
// h -> hostname client ip
func (ds *DataStore) Login(u string, p string) (*User, error) {
	user, err := ds.Storage.(*MongoDB).Login(u, p)
	if(err != nil){
		user = nil
	}
	return user, err
}

func (ds *DataStore) NextId(table string) int {
	return ds.Storage.(*MongoDB).NextId(table)
}

func (ds *DataStore) TotalErr(username string) (int, error) {
	return ds.Storage.(*MongoDB).TotalErr(username, ds.Config.Domain)
}

func (ds *DataStore) Total(username string) int {
	return ds.Storage.(*MongoDB).Total(username, ds.Config.Domain)
}

func (ds *DataStore) Unread(username string) int {
	return ds.Storage.(*MongoDB).Unread(username, ds.Config.Domain)
}

func (ds *DataStore) UnreadCount(username string) int {
	return ds.Storage.(*MongoDB).UnreadCount(username, ds.Config.Domain)
}

func (ds *DataStore) Recent(username string) int {
	return ds.Storage.(*MongoDB).Recent(username, ds.Config.Domain)
}

func (ds *DataStore) MessageSetFlags(username string, seq string) {
	ds.Storage.(*MongoDB).MessageSetFlags(username, ds.Config.Domain, seq)
}

func (ds *DataStore) MessageSetByUID(username string, set SequenceSet) Messages {
	return ds.Storage.(*MongoDB).MessageSetByUID(username, ds.Config.Domain, set)
}

func (ds *DataStore) MessageSetBySequenceNumber(username string, set SequenceSet) Messages {
	return ds.Storage.(*MongoDB).MessageSetBySequenceNumber(username, ds.Config.Domain, set)
}

// Check if email address is in greylist
// t -> type (from/to)
// m -> local mailbox
// d -> domain
// h -> client IP
func (ds *DataStore) CheckGreyMail(t, m, d, h string) bool {
	e := fmt.Sprintf("%s@%s", m, d)
	to, err := ds.Storage.(*MongoDB).IsGreyMail(e, t)
	if err != nil {
		return false
	}

	return to > 0
}

func (ds *DataStore) SaveSpamIP(ip string, email string) {
	s := SpamIP{
		Id:        bson.NewObjectId(),
		CreatedAt: time.Now(),
		IsActive:  true,
		Email:     email,
		IPAddress: ip,
	}

	if _, err := ds.Storage.(*MongoDB).StoreSpamIp(s); err != nil {
		log.LogError("Error inserting Spam IPAddress: %s", err)
	}
}

func (ds *DataStore) Pop3GetStat(username string) (int, int, error) {
	return ds.Storage.(*MongoDB).Pop3GetStat(username, ds.Config.Domain)
}

func (ds *DataStore) Pop3GetList(username string) (Messages, error) {
	return ds.Storage.(*MongoDB).Pop3GetList(username, ds.Config.Domain)
}

func (ds *DataStore) Pop3GetUidl(username string) (Messages, error) {
	return ds.Storage.(*MongoDB).Pop3GetUidl(username, ds.Config.Domain)
}

func (ds *DataStore) Pop3GetRetr(username string, sequence int) (Message, error) {
	return ds.Storage.(*MongoDB).Pop3GetRetr(username, ds.Config.Domain, sequence)
}

func (ds *DataStore) Pop3GetDele(username string, sequence int) error {
	return ds.Storage.(*MongoDB).Pop3GetDele(username, ds.Config.Domain, sequence)
}