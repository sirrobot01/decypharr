package service

import (
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/debrid"
	"github.com/sirrobot01/decypharr/pkg/repair"
	"sync"
)

type Service struct {
	Repair *repair.Repair
	Arr    *arr.Storage
	Debrid *debrid.Engine
}

var (
	instance *Service
	once     sync.Once
)

// GetService returns the singleton instance
func GetService() *Service {
	once.Do(func() {
		arrs := arr.NewStorage()
		deb := debrid.NewEngine()
		instance = &Service{
			Repair: repair.New(arrs, deb),
			Arr:    arrs,
			Debrid: deb,
		}
	})
	return instance
}

func Reset() {
	if instance != nil {
		if instance.Debrid != nil {
			instance.Debrid.Reset()
		}
		if instance.Arr != nil {
			//instance.Arr.Reset()
		}
		if instance.Repair != nil {
			//instance.Repair.Reset()
		}
	}
	once = sync.Once{}
	instance = nil
}

func GetDebrid() *debrid.Engine {
	return GetService().Debrid
}
