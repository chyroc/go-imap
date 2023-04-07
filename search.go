package imap

import (
	"reflect"
	"time"
)

// SearchReturnOption indicates what kind of results to return from a SEARCH
// command.
type SearchReturnOption string

const (
	SearchReturnMin   SearchReturnOption = "MIN"
	SearchReturnMax   SearchReturnOption = "MAX"
	SearchReturnAll   SearchReturnOption = "ALL"
	SearchReturnCount SearchReturnOption = "COUNT"
	SearchReturnSave  SearchReturnOption = "SAVE" // requires IMAP4rev2 or SEARCHRES
)

// SearchOptions contains options for the SEARCH command.
type SearchOptions struct {
	Return []SearchReturnOption // requires IMAP4rev2 or ESEARCH
}

// SearchCriteria is a criteria for the SEARCH command.
//
// When multiple fields are populated, the result is the intersection ("and"
// function) of all messages that match the fields.
type SearchCriteria struct {
	SeqNum    SeqSet
	UID       SeqSet
	SearchRes bool // requires IMAP4rev2 or SEARCHRES

	// Only the date is used, the time and timezone are ignored
	Since      time.Time
	Before     time.Time
	SentSince  time.Time
	SentBefore time.Time

	Header []SearchCriteriaHeaderField
	Body   []string
	Text   []string

	Flag    []Flag
	NotFlag []Flag

	Larger  int64
	Smaller int64

	Not []SearchCriteria
	Or  [][2]SearchCriteria
}

type SearchCriteriaHeaderField struct {
	Key, Value string
}

// SearchData is the data returned by a SEARCH command.
type SearchData struct {
	All SeqSet

	// requires IMAP4rev2 or ESEARCH
	UID   bool
	Min   uint32
	Max   uint32
	Count uint32
}

// AllNums returns All as a slice of numbers.
func (data *SearchData) AllNums() []uint32 {
	// Note: a dynamic sequence set would be a server bug
	nums, _ := data.All.Nums()
	return nums
}

// searchRes is a special empty SeqSet which can be used as a marker. It has
// a non-zero cap so that its data pointer is non-nil and can be compared.
var (
	searchRes     = make(SeqSet, 0, 1)
	searchResAddr = reflect.ValueOf(searchRes).Pointer()
)

// SearchRes returns a special marker which can be used instead of a SeqSet to
// reference the last SEARCH result. On the wire, it's encoded as '$'.
//
// It requires IMAP4rev2 or the SEARCHRES extension.
func SearchRes() SeqSet {
	return searchRes
}

// IsSearchRes checks whether a sequence set is a reference to the last SEARCH
// result. See SearchRes.
func IsSearchRes(seqSet SeqSet) bool {
	return reflect.ValueOf(seqSet).Pointer() == searchResAddr
}
