package imap

import (
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
	SeqNum SeqSet
	UID    SeqSet

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

	Not *SearchCriteria
	Or  [][2]SearchCriteria
}

// And intersects two search criteria.
func (criteria *SearchCriteria) And(other *SearchCriteria) {
	if criteria.SeqNum != nil && other.SeqNum != nil {
		criteria.SeqNum = intersectSeqSet(criteria.SeqNum, other.SeqNum)
	} else if other.SeqNum != nil {
		criteria.SeqNum = other.SeqNum
	}
	if criteria.UID != nil && other.UID != nil {
		criteria.UID = intersectSeqSet(criteria.UID, other.UID)
	} else if other.UID != nil {
		criteria.UID = other.UID
	}

	criteria.Since = intersectSince(criteria.Since, other.Since)
	criteria.Before = intersectBefore(criteria.Before, other.Before)
	criteria.SentSince = intersectSince(criteria.SentSince, other.SentSince)
	criteria.SentBefore = intersectBefore(criteria.SentBefore, other.SentBefore)

	for _, kv := range other.Header {
		criteria.Header = append(criteria.Header, kv)
	}
	for _, s := range other.Body {
		criteria.Body = append(criteria.Body, s)
	}
	for _, s := range other.Text {
		criteria.Text = append(criteria.Text, s)
	}

	for _, flag := range other.Flag {
		criteria.Flag = append(criteria.Flag, flag)
	}
	for _, flag := range other.NotFlag {
		criteria.NotFlag = append(criteria.NotFlag, flag)
	}

	if criteria.Larger == 0 || other.Larger > criteria.Larger {
		criteria.Larger = other.Larger
	}
	if criteria.Smaller == 0 || other.Smaller < criteria.Smaller {
		criteria.Smaller = other.Smaller
	}

	if criteria.Not != nil && other.Not != nil {
		criteria.Not.And(other.Not)
	} else if other.Not != nil {
		criteria.Not = other.Not
	}
	for _, or := range other.Or {
		criteria.Or = append(criteria.Or, or)
	}
}

func intersectSince(t1, t2 time.Time) time.Time {
	switch {
	case t1.IsZero():
		return t2
	case t2.IsZero():
		return t1
	case t1.After(t2):
		return t1
	default:
		return t2
	}
}

func intersectBefore(t1, t2 time.Time) time.Time {
	switch {
	case t1.IsZero():
		return t2
	case t2.IsZero():
		return t1
	case t1.Before(t2):
		return t1
	default:
		return t2
	}
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
