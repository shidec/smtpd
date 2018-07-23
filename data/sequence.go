package data

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SequenceNumber represents a  single message identifier. Could be UID or
// sequence number.
// See RFC3501 section 9.
type SequenceNumber string

// SequenceRange represents a range of identifiers. eg in IMAP: 5:9 or 15:*
type SequenceRange struct {
	Min SequenceNumber
	Max SequenceNumber
}

// SequenceSet represents set of sequence ranges. eg in IMAP: 1,3,5:9,18:*
type SequenceSet []SequenceRange

// Last returns true if this sequence number indicates the *last* sequence
// number or UID available in this mailbox.
// If false, this sequence number contains an integer value.
func (s SequenceNumber) Last() bool {
	if s == "*" {
		return true
	}
	return false
}

// Nil returns true if no sequence number was specified.
func (s SequenceNumber) Nil() bool {
	if s == "" {
		return true
	}
	return false
}

// IsValue returns true if the sequence number is a numeral value and not nil
// or *.
func (s SequenceNumber) IsValue() bool {
	return (!s.Nil() && !s.Last())
}

// Value returns the integer value of the sequence number, if any is set.
// If Nil or Last is true (ie, this sequence number is not an integer value)
// then this returns 0 and an error.
func (s SequenceNumber) Value() (uint32, error) {
	if s.Last() {
		return 0, fmt.Errorf("This sequence number indicates the last number in the mailbox and does not contain a value")
	}
	if s.Nil() {
		return 0, fmt.Errorf("This sequence number is not set")
	}

	intVal, err := strconv.ParseUint(string(s), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("Could not parse integer value of sequence number")
	}
	return uint32(intVal), nil
}

type errInvalidRangeString string
type errInvalidSequenceSetString string

func (e errInvalidRangeString) Error() string {
	return fmt.Sprintf("Invalid sequence range string '%s' specified", string(e))
}
func (e errInvalidSequenceSetString) Error() string {
	return fmt.Sprintf("Invalid sequence set string '%s' specified", string(e))
}

var rangeRegexp *regexp.Regexp
var setRegexp *regexp.Regexp

func init() {
	// Regex for finding a sequence range
	rangeRegexp = regexp.MustCompile("^(\\d{1,10}|\\*)" + // Range lower bound - digit or star
		"(?:\\:(\\d{1,10}|\\*))?$") // Range upper bound - digit or star

	// Regex for finding a sequence-set - ie, multiple sequence ranges
	setRegexp = regexp.MustCompile("^((?:\\d{1,10}|\\*)(?:\\:(?:\\d{1,10}|\\*))?)" + // First range
		"(?:" + // Match zero or more additional ranges
		"," + // Must be separated by a comma
		"((?:\\d{1,10}|\\*)(?:\\:(?:\\d{1,10}|\\*))?)" + // Additional ranges
		")*" + // Match zero or more
		"$")
}

// InterpretMessageRange creates a SequenceRange from the given string in the
// IMAP format.
func InterpretMessageRange(imapMessageRange string) (seqRange SequenceRange, err error) {
	result := rangeRegexp.FindStringSubmatch(imapMessageRange)
	if len(result) == 0 {
		return SequenceRange{}, errInvalidRangeString(imapMessageRange)
	}

	first := SequenceNumber(result[1])
	second := SequenceNumber(result[2])

	// Reduce *:* to *
	if first.Last() && second.Last() {
		return SequenceRange{Min: SequenceNumber("*"), Max: SequenceNumber("")}, nil
	}

	// Ensure "*" is always placed in 'Max'
	if first.Last() && !second.Nil() {
		return SequenceRange{Min: second, Max: first}, nil
	}

	// If both sequence numbers are integer values, we need to sort them
	if first.IsValue() && second.IsValue() {
		firstVal, _ := first.Value()
		secondVal, _ := second.Value()
		if firstVal > secondVal {
			return SequenceRange{Min: second, Max: first}, nil
		}
	}

	return SequenceRange{Min: first, Max: second}, nil
}

// InterpretSequenceSet creates a SequenceSet from the given string in the IMAP
// format.
func InterpretSequenceSet(imapSequenceSet string) (seqSet SequenceSet, err error) {
	// Ensure the sequence set is valid
	setRegexp = regexp.MustCompile("^((?:\\d{1,10}|\\*)(?:\\:(?:\\d{1,10}|\\*))?)" + // First range
		"(?:" + // Match zero or more additional ranges
		"," + // Must be separated by a comma
		"((?:\\d{1,10}|\\*)(?:\\:(?:\\d{1,10}|\\*))?)" + // Additional ranges
		")*" + // Match zero or more
		"$")
	
	if !setRegexp.MatchString(imapSequenceSet) {
		return nil, errInvalidSequenceSetString(imapSequenceSet)
	}

	ranges := strings.Split(imapSequenceSet, ",")

	seqSet = make(SequenceSet, len(ranges))
	for index, rng := range ranges {
		seqSet[index], err = InterpretMessageRange(rng)
		if err != nil {
			return seqSet, err
		}
	}

	return seqSet, nil
}
