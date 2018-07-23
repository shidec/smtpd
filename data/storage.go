package data

import (
	"fmt"
	"io"
	"time"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/log"
	"gopkg.in/mgo.v2/bson"
)

type DataStore struct {
	Config         config.DataStoreConfig
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

func (ds *DataStore) SaveMail() {
	log.LogTrace("Running SaveMail Rotuines")
	var err error
	var recon bool

	for {
		mc := <-ds.SaveMailChan
		

		if ds.Config.Storage == "mongodb" {
			id := ds.Storage.(*MongoDB).AutoInc("Messages")
			msg := ParseSMTPMessage(id, mc, mc.Domain, ds.Config.MimeParser)
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

func (ds *DataStore) Total(username string) (int, error) {
	return ds.Storage.(*MongoDB).Total(username)
}

func (ds *DataStore) Unread() (int, error) {
	return ds.Storage.(*MongoDB).Unread()
}

func (ds *DataStore) Recent() (int, error) {
	return ds.Storage.(*MongoDB).Recent()
}

func (ds *DataStore) MessageSetByUID(username string, set SequenceSet) Messages {
	return ds.Storage.(*MongoDB).MessageSetByUID(username, set)
}

func (ds *DataStore) MessageSetBySequenceNumber(username string, set SequenceSet) Messages {
	return ds.Storage.(*MongoDB).MessageSetBySequenceNumber(username, set)
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
