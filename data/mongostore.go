package data

import (
	"fmt"
	"strconv"
	"encoding/base64"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/log"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type MongoDB struct {
	Config   config.DataStoreConfig
	Session  *mgo.Session
	Messages *mgo.Collection
	Users    *mgo.Collection
	Hosts    *mgo.Collection
	Emails   *mgo.Collection
	Spamdb   *mgo.Collection
	Sequences   *mgo.Collection
}

type Sequences struct {
	table string
	value int
}

var (
	mgoSession *mgo.Session
)

func getSession(c config.DataStoreConfig) *mgo.Session {
	if mgoSession == nil {
		var err error
		mgoSession, err = mgo.Dial(c.MongoUri)
		if err != nil {
			log.LogError("Session Error connecting to MongoDB: %s", err)
			return nil
		}
	}
	return mgoSession.Clone()
}

func CreateMongoDB(c config.DataStoreConfig) *MongoDB {
	log.LogTrace("Connecting to MongoDB: %s\n", c.MongoUri)

	session, err := mgo.Dial(c.MongoUri)
	if err != nil {
		log.LogError("Error connecting to MongoDB: %s", err)
		return nil
	}

	return &MongoDB{
		Config:   c,
		Session:  session,
		Messages: session.DB(c.MongoDb).C(c.MongoColl),
		Users:    session.DB(c.MongoDb).C("Users"),
		Hosts:    session.DB(c.MongoDb).C("GreyHosts"),
		Emails:   session.DB(c.MongoDb).C("GreyMails"),
		Spamdb:   session.DB(c.MongoDb).C("SpamDB"),
		Sequences:   session.DB(c.MongoDb).C("Sequences"),
	}
}

func (mongo *MongoDB) Close() {
	mongo.Session.Close()
}


func (mongo *MongoDB) AutoInc(table string) int {
	count, _ := mongo.Sequences.Find(bson.M{"table": table}).Count()
	if count == 0 {
		mongo.Sequences.Insert(bson.M{"table": table, "value": 1})
		return 1
	}else{
		var seq *Sequences
		mongo.Sequences.Find(bson.M{"table": table}).One(&seq)
		mongo.Sequences.Update(bson.M{"table": table}, bson.M{"table": table, "value": seq.value + 1})	
		return seq.value
	}
}

func (mongo *MongoDB) Store(m *Message) (string, error) {
	err := mongo.Messages.Insert(m)
	if err != nil {
		log.LogError("Error inserting message: %s", err)
		return "", err
	}

	uEnc := base64.URLEncoding.EncodeToString([]byte(strconv.Itoa(m.Id)))
	return uEnc, nil
}

func (mongo *MongoDB) List(start int, limit int) (*Messages, error) {
	messages := &Messages{}
	err := mongo.Messages.Find(bson.M{}).Sort("-_id").Skip(start).Limit(limit).Select(bson.M{
		"id":          1,
		"from":        1,
		"to":          1,
		"attachments": 1,
		"created":     1,
		"ip":          1,
		"subject":     1,
		"starred":     1,
		"unread":      1,
		"recent":      1,
	}).All(messages)
	if err != nil {
		log.LogError("Error loading messages: %s", err)
		return nil, err
	}
	return messages, nil
}

func (mongo *MongoDB) DeleteOne(id string) error {
	_, err := mongo.Messages.RemoveAll(bson.M{"id": id})
	return err
}

func (mongo *MongoDB) DeleteAll() error {
	_, err := mongo.Messages.RemoveAll(bson.M{})
	return err
}

func (mongo *MongoDB) Load(id string) (*Message, error) {
	result := &Message{}
	err := mongo.Messages.Find(bson.M{"id": id}).One(&result)
	if err != nil {
		log.LogError("Error loading message: %s", err)
		return nil, err
	}
	return result, nil
}

func (mongo *MongoDB) Total(username string) (int, error) {
	total, err := mongo.Messages.Find(bson.M{}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total, err
}

func (mongo *MongoDB) Unread() (int, error) {
	total, err := mongo.Messages.Find(bson.M{"unread": true}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total, err
}

func (mongo *MongoDB) Recent() (int, error) {
	total, err := mongo.Messages.Find(bson.M{"recent": true}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total, err
}

func (mongo *MongoDB) LoadAttachment(id string) (*Message, error) {
	result := &Message{}
	err := mongo.Messages.Find(bson.M{"attachments.id": id}).Select(bson.M{
		"id":            1,
		"attachments.$": 1,
	}).One(&result)
	if err != nil {
		log.LogError("Error loading attachment: %s", err)
		return nil, err
	}
	return result, nil
}

//Login validates and returns a user object if they exist in the database.
func (mongo *MongoDB) Login(username, password string) (*User, error) {
	u := &User{}
	err := mongo.Users.Find(bson.M{"username": username}).One(&u)
	if err != nil {
		log.LogError("Login error: %v", err)
		return nil, err
	}

	if ok := Validate_Password(u.Password, password); !ok {
		log.LogError("Invalid Password: %s", u.Username)
		return nil, fmt.Errorf("Invalid Password!")
	}

	return u, nil
}

func (mongo *MongoDB) StoreGreyHost(h *GreyHost) (string, error) {
	err := mongo.Hosts.Insert(h)
	if err != nil {
		log.LogError("Error inserting greylist ip: %s", err)
		return "", err
	}
	return h.Id.Hex(), nil
}

func (mongo *MongoDB) StoreGreyMail(m *GreyMail) (string, error) {
	err := mongo.Emails.Insert(m)
	if err != nil {
		log.LogError("Error inserting greylist email: %s", err)
		return "", err
	}
	return m.Id.Hex(), nil
}

func (mongo *MongoDB) IsGreyHost(hostname string) (int, error) {
	tl, err := mongo.Hosts.Find(bson.M{"hostname": hostname, "isactive": true}).Count()
	if err != nil {
		log.LogError("Error checking host greylist: %s", err)
		return -1, err
	}
	return tl, nil
}

func (mongo *MongoDB) IsGreyMail(email, t string) (int, error) {
	tl, err := mongo.Emails.Find(bson.M{"email": email, "type": t, "isactive": true}).Count()
	if err != nil {
		log.LogError("Error checking email greylist: %s", err)
		return -1, err
	}
	return tl, nil
}

func (mongo *MongoDB) StoreSpamIp(s SpamIP) (string, error) {
	err := mongo.Spamdb.Insert(s)
	if err != nil {
		log.LogError("Error inserting greylist ip: %s", err)
		return "", err
	}
	return s.Id.Hex(), nil
}
/*
func (mongo *MongoDB) MessageSetByUID(username string, set SequenceSet) Messages {
	msgs := []Message {}
	msg := Message{}

	for _, msgRange := range set {
		mongo.Messages.Find(bson.M{}).One(&msg)
		msgs = append(msgs, msg)
	}
	return msgs
}
*/

func (mongo *MongoDB) MessageSetByUID(username string, set SequenceSet) Messages {
	var msgs Messages

	// If the mailbox is empty, return empty array
	count, _ := mongo.Messages.Find(bson.M{"to.mailbox":username}).Count()
	if count == 0 {
		return msgs
	}

	for _, msgRange := range set {
		msgs = append(msgs, mongo.messageRangeBy(username, msgRange)...)
	}
	return msgs
}

func (mongo *MongoDB) MessageSetBySequenceNumber(username string, set SequenceSet) Messages {
	var msgs Messages
	count , _ := mongo.Messages.Find(bson.M{"to.mailbox":username}).Count()
	if count == 0 {
		return msgs
	}
	// For each sequence range in the sequence set
	for _, msgRange := range set {
		msgs = append(msgs, mongo.messageRangeBy(username, msgRange)...)
	}
	return msgs
}

func (mongo *MongoDB) messageRangeBy(username string, seqRange SequenceRange) Messages {
	msgs := Messages {} //make([]Message, 0)
	// If Min is "*", meaning the last UID in the mailbox, Max should
	// always be Nil
	if seqRange.Min.Last() {
		msg := Message{}
		mongo.Messages.Find(bson.M{"to.mailbox" : username}).Sort("-id").Limit(1).One(&msg)
		msgs = append(msgs, msg)		
		return msgs
	}

	min, err := seqRange.Min.Value()
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())	
		return msgs
	}

	// If no Max is specified, the sequence number must be fixed
	if seqRange.Max.Nil() {
		var uid uint32
		// Fetch specific message by sequence number
		uid, _ = seqRange.Min.Value()
		msg, err := mongo.MessageByUID(username, uid)
		if err != nil {
			fmt.Printf("Error: %s\n", err.Error())
			return msgs
		}
		
		msgs = append(msgs, msg)
		return msgs
	}

	max, err := seqRange.Max.Value()
	if seqRange.Max.Last() {
		ms := Messages {}
		mongo.Messages.Find(bson.M{"to.mailbox" : username, "id" : bson.M{"$gte": min}}).Sort("-id").All(&ms)
		for _, msg := range ms {
			msgs = append(msgs, msg)	
		}
		
		return msgs
	} else {
		ms := Messages {}
		mongo.Messages.Find(bson.M{"to.mailbox" : username, "id" : bson.M{"$gte" : min, "$lte" : max}}).Sort("-nomor").All(&ms)

		for _, msg := range ms {
			msgs = append(msgs, msg)	
		}
	}
	return msgs
}

func (mongo *MongoDB) MessageByUID(username string, uid uint32) (Message, error) {
	msg := Message{}
	err := mongo.Messages.Find(bson.M{"to.mailbox" : username, "id" : uid}).One(&msg);
	if err == nil {
		return msg, nil
	}

	return msg, err
}