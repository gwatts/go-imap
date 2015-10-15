package imap

import (
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

//  08 Mar 2015 10:52:00 -0000
var msgTimeFormats = []string{
	strings.Replace(time.RFC1123, "02", "2", 1),  // Mon, 2 Jan 2006 15:04:05 MST
	strings.Replace(time.RFC1123Z, "02", "2", 1), // Mon, 2 Jan 2006 15:04:05 -0700
	strings.Replace(time.RFC822, "02", "2", 1),   // 2 Jan 06 15:04 MST
	strings.Replace(time.RFC822Z, "02", "2", 1),  // 2 Jan 06 15:04 -0700

	time.RFC1123,  // Mon, 02 Jan 2006 15:04:05 MST
	time.RFC1123Z, // Mon, 02 Jan 2006 15:04:05 -0700
	time.RFC822,   // 02 Jan 06 15:04 MST
	time.RFC822Z,  // 02 Jan 06 15:04 -0700

	// non-standard formats i've seen
	"02 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05 MST",
	//"Mon, 19 Jan 2015 01:23:42 -0800 (PST)"
}

// Address holds a single email address.
type Address struct {
	Name         string
	AtDomainList string
	MailboxName  string
	Hostname     string
	Address      string // Mailboxname@Hostname
}

// AsAddress parses an IMAP address structure as specified by RFC3501.
func AsAddress(f Field) (addr Address) {
	list, ok := f.([]Field)
	if !ok || len(list) < 4 {
		return addr
	}

	addr = Address{
		Name:         AsString(list[0]),
		AtDomainList: AsString(list[1]),
		MailboxName:  AsString(list[2]),
		Hostname:     AsString(list[3]),
	}

	// add a complete address for convenience.
	if addr.MailboxName != "" && addr.Hostname != "" {
		addr.Address = addr.MailboxName + "@" + addr.Hostname
	}
	return addr
}

// Envelope hods RFC822 email envelope data.
type Envelope struct {
	Date       time.Time
	DateString string // Unparsed datetime
	Subject    string
	From       []Address
	Sender     []Address
	ReplyTo    []Address
	To         []Address
	CC         []Address
	BCC        []Address
	InReplyTo  string
	MessageId  string
}

// AsEnvelope parses an ENVELOPE structure as specified by RFC3501.
func AsEnvelope(f Field) *Envelope {
	var err error
	var env Envelope

	list, ok := f.([]Field)
	if !ok || len(list) < 10 {
		return nil
	}

	// parse time
	// TODO: is this good enough?
	env.DateString = AsString(list[0])
	for _, fmt := range msgTimeFormats {
		s := env.DateString
		if len(s) > len(fmt) {
			s = s[:len(fmt)]
		}
		if env.Date, err = time.Parse(fmt, s); err == nil {
			break
		}
	}

	env.Subject = AsString(list[1])

	// parse the various address fields`
	for i, target := range []*[]Address{&env.From, &env.Sender, &env.ReplyTo, &env.To, &env.CC, &env.BCC} {
		if afields, ok := list[2+i].([]Field); ok {
			for _, afield := range afields {
				if field, ok := afield.([]Field); ok {
					*target = append(*target, AsAddress(field))
				}
			}
		}
	}

	env.InReplyTo = AsString(list[8])
	env.MessageId = AsString(list[9])

	return &env
}

type PartType int

const (
	BodyType = PartType(iota)
	MultipartType
)

type MessagePart interface {
	PartType() PartType
	Section() string
}

// Disposition holds the decoded message part disposition metadata encoded
// in a message part.
type Disposition struct {
	Type       string
	Attributes textproto.MIMEHeader
}

// BodyPart holds a non-multipart body structure data.
type BodyPart struct {
	// Basic fields
	Type        string // "text"
	SubType     string // "plain"
	Parameters  textproto.MIMEHeader
	ID          string
	Description string
	Encoding    string
	Size        int
	LineCount   int // number of text lines for "message" and "text" types

	// Envelope and BodyStructure are set when Type is "message"
	Envelope      *Envelope
	BodyStructure MessagePart

	// Extension data
	MD5         string
	Disposition *Disposition
	ContentMD5  string
	Language    []string
	Location    string

	section string
}

// Section returns the section identifier representing this bodypart.
// Used with FETCH to return specific parts of the message.
func (bp BodyPart) Section() string {
	return bp.section
}

// PartType always returns BodyType for BodyPart types.
func (bp BodyPart) PartType() PartType {
	return BodyType
}

// Multipart holds a mulitipart body stucture.
type Multipart struct {
	SubType     string
	Parts       []MessagePart
	Parameters  textproto.MIMEHeader
	Disposition *Disposition
	Language    []string
	Location    string
	section     string
}

// Section returns the section identifier representing this bodypart.
// Used with FETCH to return specific parts of the message.
func (mp Multipart) Section() string {
	return mp.section
}

// PartType always returns BodyType for Multipart types.
func (mp Multipart) PartType() PartType {
	return MultipartType
}

// Attachments searches for all bodyparts that are labeled with an attachment
// disposition and returns them.  If recurse is true then it will decend into
// nested multipart attachments.
func (mp Multipart) Attachments(recurse bool) (parts []*BodyPart) {
	for _, part := range mp.Parts {
		switch p := part.(type) {
		case *BodyPart:
			if p.Disposition != nil && strings.ToLower(p.Disposition.Type) == "attachment" {
				parts = append(parts, p)
			}
		case *Multipart:
			if recurse {
				parts = append(parts, p.Attachments(true)...)
			}
		}
	}
	return parts
}

func AsBodyStructure(f Field) (bs MessagePart) {
	list, ok := f.([]Field)
	if !ok || len(list) < 1 {
		return bs
	}

	if _, ok := list[0].([]Field); ok {
		// multipart
		return asMultipart(list, "")
	}
	return asBodyPart(list, "1")
}

// asMultipart parses a multipart bodystructure response.
func asMultipart(list []Field, sectionPrefix string) *Multipart {
	var (
		next int
		el   Field
		mp   Multipart
	)
	mp.section = sectionPrefix + "TEXT"

	for next, el = range list {
		section := sectionPrefix + strconv.Itoa(next+1)
		if part, ok := el.([]Field); ok {
			if _, isMultipart := part[0].([]Field); isMultipart {
				// part is itself a multipart body
				mp.Parts = append(mp.Parts, asMultipart(part, section+"."))
			} else {
				mp.Parts = append(mp.Parts, asBodyPart(part, section))
			}
		} else {
			// done reading parts; rest is metadata
			break
		}
	}

	mp.SubType = AsString(list[next])
	next++

	// read extension data, if present
	for extnum := 0; next < len(list); next++ {
		switch extnum {
		case 0:
			mp.Parameters = asAttrPairs(AsList(list[next]))
		case 1:
			// parse disposition
			mp.Disposition = asDisposition(AsList(list[next]))
		case 2:
			// parse language
			mp.Language = asLanguage(list[next])
		}
		extnum++
	}

	return &mp
}

// asBodyPart parses a bodypart bodystructure response.
func asBodyPart(list []Field, section string) *BodyPart {
	var body BodyPart

	if len(list) < 7 {
		return nil
	}

	// extract basic list
	body.section = section
	body.Type = AsString(list[0])
	body.SubType = AsString(list[1])
	body.Parameters = asAttrPairs(AsList(list[2]))
	body.ID = AsString(list[3])
	body.Description = AsString(list[4])
	body.Encoding = AsString(list[5])
	body.Size = int(AsNumber(list[6]))

	next := 7

	// parse MESSAGE or TEXT list
	btype := strings.ToLower(body.Type)
	if btype == "text" && len(list) >= 9 {
		// get line count
		body.LineCount = int(AsNumber(list[next]))
		next++

	} else if btype == "message" && strings.ToLower(body.SubType) == "rfc822" && len(list) >= 10 {
		body.Envelope = AsEnvelope(list[next])
		body.BodyStructure = AsBodyStructure(AsList(list[next+1]))
		body.LineCount = int(AsNumber(list[next+2]))
		next += 3
	}

	// parse Extension data
	for extnum := 0; next < len(list); extnum++ {
		switch extnum {
		case 0:
			body.MD5 = AsString(list[next])
		case 1:
			// parse disposition
			body.Disposition = asDisposition(AsList(list[next]))
		case 2:
			// language
			body.Language = asLanguage(list[next])
		case 3:
			// location
			body.Location = AsString(list[next])
		default:
			break
		}
		next++
	}

	return &body
}

func asAttrPairs(list []Field) textproto.MIMEHeader {
	pairs := make(textproto.MIMEHeader)
	for i := 0; i < len(list); i += 2 {
		pairs.Add(AsString(list[i]), AsString(list[i+1]))
	}
	return pairs
}

func asDisposition(list []Field) *Disposition {
	if len(list) != 2 {
		return nil
	}

	if pairs, ok := list[1].([]Field); ok {
		return &Disposition{
			Type:       AsString(list[0]),
			Attributes: asAttrPairs(pairs),
		}
	}
	return nil
}

func asLanguage(f Field) (langs []string) {
	if list, ok := f.([]Field); ok {
		for _, lang := range list {
			langs = append(langs, AsString(lang))
		}
	} else {
		langs = []string{AsString(f)}
	}
	return langs
}
