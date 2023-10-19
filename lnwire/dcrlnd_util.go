package lnwire

import "strings"

// MessagesTypesLogger logs the list of messages as a string if needed.
type MessagesTypesLogger []Message

// MessageTypesToString returns a string that describes every message type in
// the slice, separated by a comma.
func (msgs MessagesTypesLogger) String() string {
	typs := make([]string, len(msgs))
	for i := range msgs {
		typs[i] = msgs[i].MsgType().String()
	}
	return strings.Join(typs, ",")
}
