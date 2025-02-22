package imapserver

import (
	"fmt"
	"io"
	"mime"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/internal"
	"github.com/emersion/go-imap/v2/internal/imapwire"
)

func (c *Conn) handleFetch(dec *imapwire.Decoder, numKind NumKind) error {
	var seqSet imap.SeqSet
	if !dec.ExpectSP() || !dec.ExpectSeqSet(&seqSet) || !dec.ExpectSP() {
		return dec.Err()
	}

	var items []imap.FetchItem
	isList, err := dec.List(func() error {
		item, err := readFetchAtt(dec)
		if err != nil {
			return err
		}
		switch item {
		case imap.FetchItemAll, imap.FetchItemFast, imap.FetchItemFull:
			return newClientBugError("FETCH macros are not allowed in a list")
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return err
	}
	if !isList {
		item, err := readFetchAtt(dec)
		if err != nil {
			return err
		}

		// Handle macros
		switch item {
		case imap.FetchItemAll:
			items = append(items, imap.FetchItemFlags, imap.FetchItemInternalDate, imap.FetchItemRFC822Size, imap.FetchItemEnvelope)
		case imap.FetchItemFast:
			items = append(items, imap.FetchItemFlags, imap.FetchItemInternalDate, imap.FetchItemRFC822Size)
		case imap.FetchItemFull:
			items = append(items, imap.FetchItemFlags, imap.FetchItemInternalDate, imap.FetchItemRFC822Size, imap.FetchItemEnvelope, imap.FetchItemBody)
		default:
			items = append(items, item)
		}
	}

	if !dec.ExpectCRLF() {
		return dec.Err()
	}

	if err := c.checkState(imap.ConnStateSelected); err != nil {
		return err
	}

	obsolete := make(map[imap.FetchItem]imap.FetchItemKeyword)
	for i, item := range items {
		var repl imap.FetchItem
		switch item {
		case internal.FetchItemRFC822:
			repl = &imap.FetchItemBodySection{}
		case internal.FetchItemRFC822Header:
			repl = &imap.FetchItemBodySection{
				Peek:      true,
				Specifier: imap.PartSpecifierHeader,
			}
		case internal.FetchItemRFC822Text:
			repl = &imap.FetchItemBodySection{
				Specifier: imap.PartSpecifierText,
			}
		}
		if repl != nil {
			items[i] = repl
			obsolete[repl] = item.(imap.FetchItemKeyword)
		}
	}

	if numKind == NumKindUID {
		itemsWithUID := []imap.FetchItem{imap.FetchItemUID}
		for _, item := range items {
			if item != imap.FetchItemUID {
				itemsWithUID = append(itemsWithUID, item)
			}
		}
		items = itemsWithUID
	}

	w := &FetchWriter{conn: c, obsolete: obsolete}
	if err := c.session.Fetch(w, numKind, seqSet, items); err != nil {
		return err
	}
	return nil
}

func readFetchAtt(dec *imapwire.Decoder) (imap.FetchItem, error) {
	var attName string
	if !dec.Expect(dec.Func(&attName, isMsgAttNameChar), "msg-att name") {
		return nil, dec.Err()
	}
	attName = strings.ToUpper(attName)

	// Keyword fetch items are variables: return the variable so that it can be
	// compared directly
	keywords := map[imap.FetchItemKeyword]imap.FetchItem{
		imap.FetchItemAll.(imap.FetchItemKeyword):              imap.FetchItemAll,
		imap.FetchItemFast.(imap.FetchItemKeyword):             imap.FetchItemFast,
		imap.FetchItemFull.(imap.FetchItemKeyword):             imap.FetchItemFull,
		imap.FetchItemBodyStructure.(imap.FetchItemKeyword):    imap.FetchItemBodyStructure,
		imap.FetchItemEnvelope.(imap.FetchItemKeyword):         imap.FetchItemEnvelope,
		imap.FetchItemFlags.(imap.FetchItemKeyword):            imap.FetchItemFlags,
		imap.FetchItemInternalDate.(imap.FetchItemKeyword):     imap.FetchItemInternalDate,
		imap.FetchItemRFC822Size.(imap.FetchItemKeyword):       imap.FetchItemRFC822Size,
		imap.FetchItemUID.(imap.FetchItemKeyword):              imap.FetchItemUID,
		internal.FetchItemRFC822.(imap.FetchItemKeyword):       internal.FetchItemRFC822,
		internal.FetchItemRFC822Header.(imap.FetchItemKeyword): internal.FetchItemRFC822Header,
		internal.FetchItemRFC822Text.(imap.FetchItemKeyword):   internal.FetchItemRFC822Text,
	}
	if item, ok := keywords[imap.FetchItemKeyword(attName)]; ok {
		return item, nil
	}

	switch attName := imap.FetchItemKeyword(attName); attName {
	case "BINARY", "BINARY.PEEK":
		part, err := readSectionBinary(dec)
		if err != nil {
			return nil, err
		}
		partial, err := maybeReadPartial(dec)
		if err != nil {
			return nil, err
		}
		return &imap.FetchItemBinarySection{
			Part:    part,
			Partial: partial,
			Peek:    attName == "BINARY.PEEK",
		}, nil
	case "BINARY.SIZE":
		part, err := readSectionBinary(dec)
		if err != nil {
			return nil, err
		}
		return &imap.FetchItemBinarySectionSize{Part: part}, nil
	case "BODY":
		if !dec.Special('[') {
			return attName, nil
		}
		section := imap.FetchItemBodySection{}
		err := readSection(dec, &section)
		if err != nil {
			return nil, err
		}
		section.Partial, err = maybeReadPartial(dec)
		if err != nil {
			return nil, err
		}
		return &section, nil
	case "BODY.PEEK":
		if !dec.ExpectSpecial('[') {
			return nil, dec.Err()
		}
		section := imap.FetchItemBodySection{Peek: true}
		err := readSection(dec, &section)
		if err != nil {
			return nil, err
		}
		section.Partial, err = maybeReadPartial(dec)
		if err != nil {
			return nil, err
		}
		return &section, nil
	default:
		return nil, newClientBugError("Unknown FETCH data item")
	}
}

func isMsgAttNameChar(ch byte) bool {
	return ch != '[' && imapwire.IsAtomChar(ch)
}

func readSection(dec *imapwire.Decoder, section *imap.FetchItemBodySection) error {
	if dec.Special(']') {
		return nil
	}

	var dot bool
	section.Part, dot = readSectionPart(dec)
	if dot || len(section.Part) == 0 {
		var specifier string
		if dot {
			if !dec.ExpectAtom(&specifier) {
				return dec.Err()
			}
		} else {
			dec.Atom(&specifier)
		}

		switch specifier := imap.PartSpecifier(strings.ToUpper(specifier)); specifier {
		case imap.PartSpecifierNone, imap.PartSpecifierHeader, imap.PartSpecifierMIME, imap.PartSpecifierText:
			section.Specifier = specifier
		case "HEADER.FIELDS", "HEADER.FIELDS.NOT":
			if !dec.ExpectSP() {
				return dec.Err()
			}
			var err error
			headerList, err := readHeaderList(dec)
			if err != nil {
				return err
			}
			section.Specifier = imap.PartSpecifierHeader
			if specifier == "HEADER.FIELDS" {
				section.HeaderFields = headerList
			} else {
				section.HeaderFieldsNot = headerList
			}
		default:
			return newClientBugError("unknown body section specifier")
		}
	}

	if !dec.ExpectSpecial(']') {
		return dec.Err()
	}

	return nil
}

func readSectionPart(dec *imapwire.Decoder) (part []int, dot bool) {
	for {
		dot = len(part) > 0
		if dot && !dec.Special('.') {
			return part, false
		}

		var num uint32
		if !dec.Number(&num) {
			return part, dot
		}
		part = append(part, int(num))
	}
}

func readHeaderList(dec *imapwire.Decoder) ([]string, error) {
	var l []string
	err := dec.ExpectList(func() error {
		var s string
		if !dec.ExpectAString(&s) {
			return dec.Err()
		}
		l = append(l, s)
		return nil
	})
	return l, err
}

func readSectionBinary(dec *imapwire.Decoder) ([]int, error) {
	if !dec.ExpectSpecial('[') {
		return nil, dec.Err()
	}
	if dec.Special(']') {
		return nil, nil
	}

	var l []int
	for {
		var num uint32
		if !dec.ExpectNumber(&num) {
			return l, dec.Err()
		}
		l = append(l, int(num))

		if !dec.Special('.') {
			break
		}
	}

	if !dec.ExpectSpecial(']') {
		return l, dec.Err()
	}
	return l, nil
}

func maybeReadPartial(dec *imapwire.Decoder) (*imap.SectionPartial, error) {
	if !dec.Special('<') {
		return nil, nil
	}
	var partial imap.SectionPartial
	if !dec.ExpectNumber64(&partial.Offset) || !dec.ExpectSpecial('.') || !dec.ExpectNumber64(&partial.Size) || !dec.ExpectSpecial('>') {
		return nil, dec.Err()
	}
	return &partial, nil
}

// FetchWriter writes FETCH responses.
type FetchWriter struct {
	conn     *Conn
	obsolete map[imap.FetchItem]imap.FetchItemKeyword
}

// CreateMessage writes a FETCH response for a message.
//
// FetchResponseWriter.Close must be called.
func (cmd *FetchWriter) CreateMessage(seqNum uint32) *FetchResponseWriter {
	enc := newResponseEncoder(cmd.conn)
	enc.Atom("*").SP().Number(seqNum).SP().Atom("FETCH").SP().Special('(')
	return &FetchResponseWriter{enc: enc, obsolete: cmd.obsolete}
}

// FetchResponseWriter writes a single FETCH response for a message.
type FetchResponseWriter struct {
	enc      *responseEncoder
	hasItem  bool
	obsolete map[imap.FetchItem]imap.FetchItemKeyword
}

func (w *FetchResponseWriter) writeItemSep() {
	if w.hasItem {
		w.enc.SP()
	}
	w.hasItem = true
}

// WriteUID writes the message's UID.
func (w *FetchResponseWriter) WriteUID(uid uint32) {
	w.writeItemSep()
	w.enc.Atom("UID").SP().Number(uid)
}

// WriteFlags writes the message's flags.
func (w *FetchResponseWriter) WriteFlags(flags []imap.Flag) {
	w.writeItemSep()
	w.enc.Atom("FLAGS").SP().List(len(flags), func(i int) {
		w.enc.Flag(flags[i])
	})
}

// WriteRFC822Size writes the message's full size.
func (w *FetchResponseWriter) WriteRFC822Size(size int64) {
	w.writeItemSep()
	w.enc.Atom("RFC822.SIZE").SP().Number64(size)
}

// WriteInternalDate writes the message's internal date.
func (w *FetchResponseWriter) WriteInternalDate(t time.Time) {
	w.writeItemSep()
	w.enc.Atom("INTERNALDATE").SP().String(t.Format(internal.DateTimeLayout))
}

// WriteBodySection writes a body section.
//
// The returned io.WriteCloser must be closed before writing any more message
// data items.
func (w *FetchResponseWriter) WriteBodySection(section *imap.FetchItemBodySection, size int64) io.WriteCloser {
	w.writeItemSep()
	enc := w.enc.Encoder

	if obs, ok := w.obsolete[section]; ok {
		enc.Atom(string(obs))
	} else {
		writeItemBodySection(enc, section)
	}

	enc.SP()
	return w.enc.Literal(size)
}

func writeItemBodySection(enc *imapwire.Encoder, section *imap.FetchItemBodySection) {
	enc.Atom("BODY")
	enc.Special('[')
	writeSectionPart(enc, section.Part)
	if len(section.Part) > 0 && section.Specifier != imap.PartSpecifierNone {
		enc.Special('.')
	}
	if section.Specifier != imap.PartSpecifierNone {
		enc.Atom(string(section.Specifier))

		var headerList []string
		if len(section.HeaderFields) > 0 {
			headerList = section.HeaderFields
			enc.Atom(".FIELDS")
		} else if len(section.HeaderFieldsNot) > 0 {
			headerList = section.HeaderFieldsNot
			enc.Atom(".FIELDS.NOT")
		}

		if len(headerList) > 0 {
			enc.SP().List(len(headerList), func(i int) {
				enc.String(headerList[i])
			})
		}
	}
	enc.Special(']')
	if partial := section.Partial; partial != nil {
		enc.Special('<').Number(uint32(partial.Offset)).Special('>')
	}
}

// WriteBinarySection writes a binary section.
//
// The returned io.WriteCloser must be closed before writing any more message
// data items.
func (w *FetchResponseWriter) WriteBinarySection(section *imap.FetchItemBinarySection, size int64) io.WriteCloser {
	w.writeItemSep()
	enc := w.enc.Encoder

	enc.Atom("BINARY").Special('[')
	writeSectionPart(enc, section.Part)
	enc.Special(']').SP()
	enc.Special('~') // indicates literal8
	return w.enc.Literal(size)
}

// WriteBinarySectionSize writes a binary section size.
func (w *FetchResponseWriter) WriteBinarySectionSize(section *imap.FetchItemBinarySection, size uint32) {
	w.writeItemSep()
	enc := w.enc.Encoder

	enc.Atom("BINARY.SIZE").Special('[')
	writeSectionPart(enc, section.Part)
	enc.Special(']').SP().Number(size)
}

// WriteEnvelope writes the message's envelope.
func (w *FetchResponseWriter) WriteEnvelope(envelope *imap.Envelope) {
	w.writeItemSep()
	enc := w.enc.Encoder
	enc.Atom("ENVELOPE").SP()
	writeEnvelope(enc, envelope)
}

// WriteBodyStructure writes the message's body structure (either BODYSTRUCTURE
// or BODY).
func (w *FetchResponseWriter) WriteBodyStructure(bs imap.BodyStructure) {
	var extended bool
	switch bs := bs.(type) {
	case *imap.BodyStructureSinglePart:
		extended = bs.Extended != nil
	case *imap.BodyStructureMultiPart:
		extended = bs.Extended != nil
	}

	item := "BODY"
	if extended {
		item = "BODYSTRUCTURE"
	}

	w.writeItemSep()
	enc := w.enc.Encoder
	enc.Atom(item).SP()
	writeBodyStructure(enc, bs)
}

// Close closes the FETCH message writer.
func (w *FetchResponseWriter) Close() error {
	if w.enc == nil {
		return fmt.Errorf("imapserver: FetchResponseWriter already closed")
	}
	err := w.enc.Special(')').CRLF()
	w.enc.end()
	w.enc = nil
	return err
}

func writeEnvelope(enc *imapwire.Encoder, envelope *imap.Envelope) {
	if envelope == nil {
		envelope = new(imap.Envelope)
	}

	sender := envelope.Sender
	if sender == nil {
		sender = envelope.From
	}
	replyTo := envelope.ReplyTo
	if replyTo == nil {
		replyTo = envelope.From
	}

	enc.Special('(')
	writeNString(enc, envelope.Date)
	enc.SP()
	writeNString(enc, mime.QEncoding.Encode("utf-8", envelope.Subject))
	addrs := [][]imap.Address{
		envelope.From,
		sender,
		replyTo,
		envelope.To,
		envelope.Cc,
		envelope.Bcc,
	}
	for _, l := range addrs {
		enc.SP()
		writeAddressList(enc, l)
	}
	enc.SP()
	writeNString(enc, envelope.InReplyTo)
	enc.SP()
	writeNString(enc, envelope.MessageID)
	enc.Special(')')
}

func writeAddressList(enc *imapwire.Encoder, l []imap.Address) {
	if l == nil {
		enc.NIL()
		return
	}

	enc.List(len(l), func(i int) {
		addr := l[i]
		enc.Special('(')
		writeNString(enc, mime.QEncoding.Encode("utf-8", addr.Name))
		enc.SP().NIL().SP()
		writeNString(enc, addr.Mailbox)
		enc.SP()
		writeNString(enc, addr.Host)
		enc.Special(')')
	})
}

func writeNString(enc *imapwire.Encoder, s string) {
	if s == "" {
		enc.NIL()
	} else {
		enc.String(s)
	}
}

func writeSectionPart(enc *imapwire.Encoder, part []int) {
	if len(part) == 0 {
		return
	}

	var l []string
	for _, num := range part {
		l = append(l, fmt.Sprintf("%v", num))
	}
	enc.Atom(strings.Join(l, "."))
}

func writeBodyStructure(enc *imapwire.Encoder, bs imap.BodyStructure) {
	enc.Special('(')
	switch bs := bs.(type) {
	case *imap.BodyStructureSinglePart:
		writeBodyType1part(enc, bs)
	case *imap.BodyStructureMultiPart:
		writeBodyTypeMpart(enc, bs)
	default:
		panic(fmt.Errorf("unknown body structure type %T", bs))
	}
	enc.Special(')')
}

func writeBodyType1part(enc *imapwire.Encoder, bs *imap.BodyStructureSinglePart) {
	enc.String(bs.Type).SP().String(bs.Subtype).SP()
	writeBodyFldParam(enc, bs.Params)
	enc.SP()
	writeNString(enc, bs.ID)
	enc.SP()
	writeNString(enc, bs.Description)
	enc.SP()
	if bs.Encoding == "" {
		enc.String("7BIT")
	} else {
		enc.String(strings.ToUpper(bs.Encoding))
	}
	enc.SP().Number(bs.Size)

	if msg := bs.MessageRFC822; msg != nil {
		enc.SP()
		writeEnvelope(enc, msg.Envelope)
		enc.SP()
		writeBodyStructure(enc, msg.BodyStructure)
		enc.SP().Number64(msg.NumLines)
	} else if text := bs.Text; text != nil {
		enc.SP().Number64(text.NumLines)
	}

	ext := bs.Extended
	if ext == nil {
		return
	}

	enc.SP()
	enc.NIL() // MD5
	enc.SP()
	writeBodyFldDsp(enc, ext.Disposition)
	enc.SP()
	writeBodyFldLang(enc, ext.Language)
	enc.SP()
	writeNString(enc, ext.Location)
}

func writeBodyTypeMpart(enc *imapwire.Encoder, bs *imap.BodyStructureMultiPart) {
	if len(bs.Children) == 0 {
		panic("imapserver: imap.BodyStructureMultiPart must have at least one child")
	}
	for i, child := range bs.Children {
		if i > 0 {
			enc.SP()
		}
		writeBodyStructure(enc, child)
	}

	enc.SP().String(bs.Subtype)

	ext := bs.Extended
	if ext == nil {
		return
	}

	enc.SP()
	writeBodyFldParam(enc, ext.Params)
	enc.SP()
	writeBodyFldDsp(enc, ext.Disposition)
	enc.SP()
	writeBodyFldLang(enc, ext.Language)
	enc.SP()
	writeNString(enc, ext.Location)
}

func writeBodyFldParam(enc *imapwire.Encoder, params map[string]string) {
	if params == nil {
		enc.NIL()
		return
	}

	var l []string
	for k := range params {
		l = append(l, k)
	}
	sort.Strings(l)

	enc.List(len(l), func(i int) {
		k := l[i]
		v := params[k]
		enc.String(k).SP().String(v)
	})
}

func writeBodyFldDsp(enc *imapwire.Encoder, disp *imap.BodyStructureDisposition) {
	if disp == nil {
		enc.NIL()
		return
	}

	enc.Special('(').String(disp.Value).SP()
	writeBodyFldParam(enc, disp.Params)
	enc.Special(')')
}

func writeBodyFldLang(enc *imapwire.Encoder, l []string) {
	if l == nil {
		enc.NIL()
	} else {
		enc.List(len(l), func(i int) {
			enc.String(l[i])
		})
	}
}
