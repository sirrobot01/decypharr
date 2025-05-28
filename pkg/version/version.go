package version

import "fmt"

type Info struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
}

func (i Info) String() string {
	return fmt.Sprintf("%s-%s", i.Version, i.Channel)
}

var (
	Version = ""
	Channel = ""
)

func GetInfo() Info {
	return Info{
		Version: Version,
		Channel: Channel,
	}
}
