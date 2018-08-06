package data

import (
	"fmt"

	"github.com/shidec/smtpd/config"
	"github.com/shidec/smtpd/log"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type MongoDB struct {
	Config 	 config.DataStoreConfig
	Session  *mgo.Session
	Messages *mgo.Collection
	Users    *mgo.Collection
	Hosts    *mgo.Collection
	Emails   *mgo.Collection
	Mailboxes   *mgo.Collection
	Spamdb   *mgo.Collection
	Sequences   *mgo.Collection
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
		Mailboxes:   session.DB(c.MongoDb).C("Mailboxes"),
		Sequences:   session.DB(c.MongoDb).C("Sequences"),
	}
}

func (mongo *MongoDB) Close() {
	mongo.Session.Close()
}

func (mongo *MongoDB) NextId(username string) int {
	count, _ := mongo.Messages.Find(bson.M{"to.mailbox": username}).Count()
	if count == 0 {
		return 1
	}else{
		var m Message
		mongo.Messages.Find(bson.M{"to.mailbox": username}).Select(bson.M{"sequence" : 1}).Sort("-sequence").Limit(1).One(&m)
		return m.Sequence + 1
	}
}

func (mongo *MongoDB) RemoveRecent(m Message) {
	mongo.Messages.Update(bson.M{"id": m.Id}, bson.M{"$set": bson.M{"recent": false}})
}

func (mongo *MongoDB) Store(m *Message) (string, error) {
	err := mongo.Messages.Insert(m)
	if err != nil {
		log.LogError("Error inserting message: %s", err)
		return "", err
	}

	uEnc := m.Id
	return uEnc, nil
}

func (mongo *MongoDB) List(username, domain string, start int, limit int) (*Messages, error) {
	log.LogError("List: %d : %d", start, limit)
	messages := &Messages{}
	err := mongo.Messages.Find(bson.M{"to.mailbox": username, "to.domain": domain}).Sort("-_id").Skip(start).Limit(limit).Select(bson.M{
		"id":          1,
		"sequence":    1,
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

func (mongo *MongoDB) TotalErr(username string) (int, error) {
	total, err := mongo.Messages.Find(bson.M{"to.mailbox": username}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total, err
}

func (mongo *MongoDB) Total(username string) int {
	total, err := mongo.Messages.Find(bson.M{"to.mailbox": username}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total
}

func (mongo *MongoDB) Unread(username string) int {
	total, _ := mongo.Messages.Find(bson.M{"to.mailbox": username,"unread": true}).Count()
	if total == 0 {
		return 0
	}

	var m Message
	mongo.Messages.Find(bson.M{"to.mailbox": username, "unread": true}).Select(bson.M{"sequence" : 1}).Sort("-sequence").Limit(1).One(&m)
	return m.Sequence
}

func (mongo *MongoDB) UnreadCount(username string) int {
	total, err := mongo.Messages.Find(bson.M{"to.mailbox": username,"unread": true}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total
}

func (mongo *MongoDB) Recent(username string) int {
	total, err := mongo.Messages.Find(bson.M{"to.mailbox": username,"recent": true}).Count()
	if err != nil {
		log.LogError("Error loading message: %s", err)
		total = 0
	}
	return total
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
		fmt.Printf("Range:" + string(msgRange.Min) + ":" + string(msgRange.Max))	
		msgs = append(msgs, mongo.messageRangeBy(username, msgRange)...)
	}
	return msgs
}

func (mongo *MongoDB) messageRangeBy(username string, seqRange SequenceRange) Messages {
	msgs := Messages {} //make([]Message, 0)
	// If Min is "*", meaning the last UID in the mailbox, Max should
	// always be Nil
	if seqRange.Min.Last() {
		fmt.Printf("messageRangeBy:last")
		msg := Message{}
		mongo.Messages.Find(bson.M{"to.mailbox" : username}).Sort("-sequence").Limit(1).One(&msg)
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
		mongo.Messages.Find(bson.M{"to.mailbox" : username, "sequence" : bson.M{"$gte": min}}).Sort("-sequence").All(&ms)
		for _, msg := range ms {
			msgs = append(msgs, msg)	
		}
		
		return msgs
	} else {
		ms := Messages {}
		mongo.Messages.Find(bson.M{"to.mailbox" : username, "sequence" : bson.M{"$gte" : min, "$lte" : max}}).Sort("-sequence").All(&ms)

		for _, msg := range ms {
			msgs = append(msgs, msg)	
		}
	}
	return msgs
}

func (mongo *MongoDB) MessageByUID(username string, uid uint32) (Message, error) {
	msg := Message{}
	err := mongo.Messages.Find(bson.M{"to.mailbox" : username, "sequence" : uid}).One(&msg);
	if err == nil {
		return msg, nil
	}

	return msg, err
}

func (mongo *MongoDB) Pop3GetStat(username string, domain string) (int, int, error) {
	msg := StatQuery{}

	err := mongo.Messages.Pipe([]bson.M{bson.M{"$match": bson.M{"to.mailbox": username, "to.domain": domain}},
		bson.M{"$group": bson.M{"_id": nil, "count": bson.M{"$sum":1}, "total": bson.M{"$sum":"$content.size"}}}}).One(&msg)

	if err != nil {
		log.LogError("Error loading messages: %s", err)
		return 0, 0, err
	}
	return msg.Count, msg.Total, nil
}

func (mongo *MongoDB) Pop3GetList(username string, domain string) (Messages, error)  {
	msgs := Messages{}

	err := mongo.Messages.Find(bson.M{"to.mailbox": username, "to.domain": domain}).Select(bson.M{"sequence": 1, "content.size": 1}).All(&msgs)

	if err != nil {
		log.LogError("Error loading messages: %s", err)
		return msgs, err
	}
	return msgs, nil
}

func (mongo *MongoDB) Pop3GetUidl(username string, domain string) (Messages, error)  {
	msgs := Messages{}

	err := mongo.Messages.Find(bson.M{"to.mailbox": username, "to.domain": domain}).Select(bson.M{"sequence": 1, "id": 1}).All(&msgs)

	if err != nil {
		log.LogError("Error loading messages: %s", err)
		return msgs, err
	}
	return msgs, nil
}

func (mongo *MongoDB) Pop3GetRetr(username string, domain string, sequence int) (Message, error)  {
	msg := Message{}
	err := mongo.Messages.Find(bson.M{"to.mailbox": username, "to.domain": domain, "sequence": sequence}).One(&msg)

	if err != nil {
		log.LogError("Error loading messages: %s", err)
		return msg, err
	}
	return msg, nil
}

func (mongo *MongoDB) Pop3GetDele(username string, domain string, sequence int) error  {
	err := mongo.Messages.Remove(bson.M{"to.mailbox": username, "to.domain": domain, "sequence": sequence})

	if err != nil {
		log.LogError("Error loading messages: %s", err)
	}
	return err
}