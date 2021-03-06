package pkg

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/tada/catch"

	"github.com/tada/catch/pio"
	"github.com/tada/jsonstream"
	"github.com/tada/mqtt-nats/mqtt"
)

const (
	// PublishRetain is the bit representing MQTT PUBLISH "retain" flag
	PublishRetain = 0x01

	// PublishQoS is the mask for the MQTT PUBLISH "quality of service" bits
	PublishQoS = 0x06

	// PublishDup is the bit representing MQTT PUBLISH "dup" flag
	PublishDup = 0x08
)

// The Publish type represents the MQTT PUBLISH packet
type Publish struct {
	name     string
	replyTo  string
	payload  []byte
	id       uint16
	flags    byte
	sentByUs bool // set if the message originated from this server (happens when a client will is published)
}

// SimplePublish creates a new Publish packet with all flags zero and no reply
func SimplePublish(topic string, payload []byte) *Publish {
	return &Publish{name: topic, payload: payload}
}

// NewPublish creates a new Publish packet
func NewPublish(id uint16, topic string, flags byte, payload []byte, sentByUs bool, natsReplyTo string) *Publish {
	return &Publish{
		name:     topic,
		replyTo:  natsReplyTo,
		payload:  payload,
		id:       id,
		flags:    flags,
		sentByUs: sentByUs,
	}
}

// NewPublish2 creates a new Publish packet
func NewPublish2(id uint16, topic string, payload []byte, qos byte, dup bool, retain bool) *Publish {
	flags := byte(0)
	if qos > 0 {
		flags |= qos << 1
	}
	if dup {
		flags |= PublishDup
	}
	if retain {
		flags |= PublishRetain
	}
	return &Publish{id: id, flags: flags, name: topic, payload: payload, sentByUs: false, replyTo: ""}
}

// ParsePublish parses the publish packet from the given reader.
func ParsePublish(r *mqtt.Reader, flags byte, pkLen int) (Packet, error) {
	var err error
	if r, err = r.ReadPacket(pkLen); err != nil {
		return nil, err
	}

	pp := &Publish{flags: flags & 0xf}
	if pp.name, err = r.ReadString(); err != nil {
		return nil, err
	}

	if pp.QoSLevel() > 0 {
		if pp.id, err = r.ReadUint16(); err != nil {
			return nil, err
		}
	}
	if pp.payload, err = r.ReadRemainingBytes(); err != nil {
		return nil, err
	}
	return pp, nil
}

// Equals returns true if this packet is equal to the given packet, false if not
func (p *Publish) Equals(other interface{}) bool {
	op, ok := other.(*Publish)
	return ok &&
		p.id == op.id &&
		p.flags == op.flags &&
		p.sentByUs == op.sentByUs &&
		p.name == op.name &&
		p.replyTo == op.replyTo &&
		bytes.Equal(p.payload, op.payload)
}

// Flags returns the packet flags
func (p *Publish) Flags() byte {
	return p.flags
}

// ID returns the MQTT Packet Identifier. The identifier is only valid if QoS > 0
func (p *Publish) ID() uint16 {
	return p.id
}

// IsDup returns true if the packet is a duplicate of a previously sent packet
func (p *Publish) IsDup() bool {
	return (p.flags & PublishDup) != 0
}

// SetDup sets the dup flag of the packet
func (p *Publish) SetDup() {
	p.flags |= PublishDup
}

// IsPrintableASCII returns true if the given bytes are constrained to the ASCII 7-bit character set and
// has no control characters.
func IsPrintableASCII(bs []byte) bool {
	for i := range bs {
		c := bs[i]
		if c < 32 || c > 127 {
			return false
		}
	}
	return true
}

// MarshalToJSON marshals the packet as a JSON object onto the given writer
func (p *Publish) MarshalToJSON(w io.Writer) {
	pio.WriteString(w, `{"flags":`)
	pio.WriteInt(w, int64(p.flags))
	pio.WriteString(w, `,"id":`)
	pio.WriteInt(w, int64(p.id))
	pio.WriteString(w, `,"name":`)
	jsonstream.WriteString(w, p.name)
	if p.replyTo != "" {
		pio.WriteString(w, `,"replyTo":`)
		jsonstream.WriteString(w, p.replyTo)
	}
	if len(p.payload) > 0 {
		if IsPrintableASCII(p.payload) {
			pio.WriteString(w, `,"payload":`)
			jsonstream.WriteString(w, string(p.payload))
		} else {
			pio.WriteString(w, `,"payloadEnc":`)
			jsonstream.WriteString(w, base64.StdEncoding.EncodeToString(p.payload))
		}
	}
	pio.WriteByte(w, '}')
}

// NatsReplyTo returns the NATS replyTo subject. Only valid when the packet represents something
// received from NATS due to a client subscribing to a topic with QoS level > 0
func (p *Publish) NatsReplyTo() string {
	return p.replyTo
}

// Payload returns the payload of the published message
func (p *Publish) Payload() []byte {
	return p.payload
}

// QoSLevel returns the quality of service level which is 0, 1 or 2.
func (p *Publish) QoSLevel() byte {
	return (p.flags & PublishQoS) >> 1
}

// ResetRetain resets the retain flag
func (p *Publish) ResetRetain() {
	p.flags &^= PublishRetain
}

// Retain returns the retain flag setting
func (p *Publish) Retain() bool {
	return (p.flags & PublishRetain) != 0
}

// String returns a brief string representation of the packet. Suitable for logging
func (p *Publish) String() string {
	// layout borrowed from mosquitto_sub log output
	return fmt.Sprintf("PUBLISH (d%d, q%d, r%b, m%d, '%s', ... (%d bytes))",
		(p.flags&0x08)>>3,
		p.QoSLevel(),
		p.flags&0x01,
		p.ID(),
		p.name,
		len(p.payload))
}

// TopicName returns the name of the topic
func (p *Publish) TopicName() string {
	return p.name
}

// UnmarshalFromJSON expects the given token to be the object start '{'. If it is, the rest
// of the object is unmarshalled into the receiver. The method will panic with a pio.Error
// if any errors are detected.
//
// See jsonstreamer.Consumer for more info.
func (p *Publish) UnmarshalFromJSON(js jsonstream.Decoder, t json.Token) {
	jsonstream.AssertDelim(t, '{')
	for {
		s, ok := js.ReadStringOrEnd('}')
		if !ok {
			break
		}
		switch s {
		case "flags":
			p.flags = byte(js.ReadInt())
		case "id":
			p.id = uint16(js.ReadInt())
		case "name":
			p.name = js.ReadString()
		case "replyTo":
			p.replyTo = js.ReadString()
		case "payload":
			p.payload = []byte(js.ReadString())
		case "payloadEnc":
			var err error
			p.payload, err = base64.StdEncoding.DecodeString(js.ReadString())
			if err != nil {
				panic(catch.Error(err))
			}
		}
	}
}

// Write writes the MQTT bits of this packet on the given Writer
func (p *Publish) Write(w *mqtt.Writer) {
	w.WriteU8(TpPublish | p.flags)
	pkLen := 2 + len(p.name) + len(p.payload)
	if p.QoSLevel() > 0 {
		pkLen += 2
	}
	w.WriteVarInt(pkLen)
	w.WriteString(p.name)
	if p.QoSLevel() > 0 {
		w.WriteU16(p.id)
	}
	_, _ = w.Write(p.payload)
}

// The PubAck type represents the MQTT PUBACK packet
type PubAck uint16

// ParsePubAck parses a PUBACK packet
func ParsePubAck(r *mqtt.Reader, _ byte, pkLen int) (Packet, error) {
	if pkLen != 2 {
		return PubAck(0), errors.New("malformed PUBACK")
	}
	id, err := r.ReadUint16()
	return PubAck(id), err
}

// Equals returns true if this packet is equal to the given packet, false if not
func (p PubAck) Equals(other interface{}) bool {
	return p == other
}

// ID returns the packet ID
func (p PubAck) ID() uint16 {
	return uint16(p)
}

// String returns a brief string representation of the packet. Suitable for logging
func (p PubAck) String() string {
	return fmt.Sprintf("PUBACK (m%d)", p.ID())
}

// Write writes the MQTT bits of this packet on the given Writer
func (p PubAck) Write(w *mqtt.Writer) {
	w.WriteU8(TpPubAck)
	w.WriteU8(2)
	w.WriteU16(uint16(p))
}

// The PubRec type represents the MQTT PUBREC packet
type PubRec uint16

// ParsePubRec parses a PUBREC packet
func ParsePubRec(r *mqtt.Reader, _ byte, pkLen int) (Packet, error) {
	if pkLen != 2 {
		return PubRec(0), errors.New("malformed PUBREC")
	}
	id, err := r.ReadUint16()
	return PubRec(id), err
}

// Equals returns true if this packet is equal to the given packet, false if not
func (p PubRec) Equals(other interface{}) bool {
	return p == other
}

// ID returns the packet ID
func (p PubRec) ID() uint16 {
	return uint16(p)
}

// String returns a brief string representation of the packet. Suitable for logging
func (p PubRec) String() string {
	return fmt.Sprintf("PUBREC (m%d)", p.ID())
}

// Write writes the MQTT bits of this packet on the given Writer
func (p PubRec) Write(w *mqtt.Writer) {
	w.WriteU8(TpPubRec)
	w.WriteU8(2)
	w.WriteU16(uint16(p))
}

// The PubRel type represents the MQTT PUBREL packet
type PubRel uint16

// ParsePubRel parses a PUBREL packet
func ParsePubRel(r *mqtt.Reader, _ byte, pkLen int) (Packet, error) {
	if pkLen != 2 {
		return PubRel(0), errors.New("malformed PUBREL")
	}
	id, err := r.ReadUint16()
	return PubRel(id), err
}

// Equals returns true if this packet is equal to the given packet, false if not
func (p PubRel) Equals(other interface{}) bool {
	return p == other
}

// ID returns the packet ID
func (p PubRel) ID() uint16 {
	return uint16(p)
}

// String returns a brief string representation of the packet. Suitable for logging
func (p PubRel) String() string {
	return fmt.Sprintf("PUBREL (m%d)", p.ID())
}

// Write writes the MQTT bits of this packet on the given Writer
func (p PubRel) Write(w *mqtt.Writer) {
	w.WriteU8(TpPubRel)
	w.WriteU8(2)
	w.WriteU16(uint16(p))
}

// The PubComp type represents the MQTT PUBCOMP packet
type PubComp uint16

// ParsePubComp parses a PUBCOMP packet
func ParsePubComp(r *mqtt.Reader, _ byte, pkLen int) (Packet, error) {
	if pkLen != 2 {
		return PubComp(0), errors.New("malformed PUBCOMP")
	}
	id, err := r.ReadUint16()
	return PubComp(id), err
}

// Equals returns true if this packet is equal to the given packet, false if not
func (p PubComp) Equals(other interface{}) bool {
	return p == other
}

// ID returns the packet ID
func (p PubComp) ID() uint16 {
	return uint16(p)
}

// String returns a brief string representation of the packet. Suitable for logging
func (p PubComp) String() string {
	return fmt.Sprintf("PUBCOMP (m%d)", p.ID())
}

// Write writes the MQTT bits of this packet on the given Writer
func (p PubComp) Write(w *mqtt.Writer) {
	w.WriteU8(TpPubComp)
	w.WriteU8(2)
	w.WriteU16(uint16(p))
}
