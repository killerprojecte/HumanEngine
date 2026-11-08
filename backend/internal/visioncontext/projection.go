package visioncontext

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// Project preserves canonical WTF-8 descriptions and the source's property
// presence/order. Native consumers must use this instead of a JSON round-trip.
func (r Result) Project() (*jsjson.Object, error) {
	images := make([]any, 0, len(r.Images))
	for _, i := range r.Images {
		v, e := i.Project()
		if e != nil {
			return nil, e
		}
		images = append(images, v)
	}
	omitted := make([]any, 0, len(r.Omitted))
	for _, i := range r.Omitted {
		v, e := jsjson.NewObject(jsjson.Member{Key: "messageId", Value: i.MessageID}, jsjson.Member{Key: "reason", Value: i.Reason})
		if e != nil {
			return nil, e
		}
		omitted = append(omitted, v)
	}
	return jsjson.NewObject(jsjson.Member{Key: "trust", Value: r.Trust}, jsjson.Member{Key: "framePolicy", Value: r.FramePolicy}, jsjson.Member{Key: "images", Value: images}, jsjson.Member{Key: "omitted", Value: omitted})
}
func (o Observation) members() []jsjson.Member {
	m := []jsjson.Member{{Key: "status", Value: o.Status}}
	if o.Description != "" {
		m = append(m, jsjson.Member{Key: "description", Value: o.Description})
	}
	if o.Reason != "" {
		m = append(m, jsjson.Member{Key: "reason", Value: o.Reason})
	}
	if o.FramePolicy != "" {
		m = append(m, jsjson.Member{Key: "framePolicy", Value: o.FramePolicy})
	}
	return m
}
func (i Image) Project() (*jsjson.Object, error) {
	return jsjson.NewObject(append([]jsjson.Member{{Key: "messageId", Value: i.MessageID}}, i.Observation.members()...)...)
}
func (o Observation) MarshalJSON() ([]byte, error) {
	v, e := jsjson.NewObject(o.members()...)
	if e != nil {
		return nil, e
	}
	return jsjson.Marshal(v)
}
func (i Image) MarshalJSON() ([]byte, error) {
	v, e := i.Project()
	if e != nil {
		return nil, e
	}
	return jsjson.Marshal(v)
}

// MarshalJSON is also safe for encoding/json callers: lone surrogates are
// emitted as JSON escapes by jsjson, never replaced with U+FFFD.
func (r Result) MarshalJSON() ([]byte, error) {
	v, e := r.Project()
	if e != nil {
		return nil, e
	}
	return jsjson.Marshal(v)
}
