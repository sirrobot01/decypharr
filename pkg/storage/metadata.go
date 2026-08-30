package storage

import (
	"strconv"

	"github.com/sirrobot01/appendstore"
)

const (
	attributeCategory  = "category"
	attributeProvider  = "provider"
	attributeStatus    = "status"
	attributeName      = "name"
	attributeTotalSize = "total_size"
	attributeProtocol  = "protocol"
	attributeBad       = "bad"
	attributeAddedOn   = "added_on"
)

func entryPutOptions(entry *Entry) *appendstore.PutOptions {
	return &appendstore.PutOptions{Attributes: map[string]string{
		attributeCategory:  entry.Category,
		attributeProvider:  entry.ActiveProvider,
		attributeStatus:    string(entry.Status),
		attributeName:      entry.GetFolder(),
		attributeTotalSize: strconv.FormatInt(entry.Size, 10),
		attributeProtocol:  string(entry.Protocol),
		attributeBad:       strconv.FormatBool(entry.Bad),
		attributeAddedOn:   strconv.FormatInt(entry.AddedOn.Unix(), 10),
	}}
}

func metadataInt64(meta *appendstore.Metadata, attribute string) int64 {
	value, _ := strconv.ParseInt(meta.Attribute(attribute), 10, 64)
	return value
}

func metadataBool(meta *appendstore.Metadata, attribute string) bool {
	value, _ := strconv.ParseBool(meta.Attribute(attribute))
	return value
}
