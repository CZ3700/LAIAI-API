package main

import (
	"fmt"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
)

func main() {
	p, err := arkverification.Load(ark.Model25Flash)
	if err != nil {
		fmt.Println("ERR:", err)
		return
	}
	fmt.Println("EvidenceSHA256:", p.EvidenceSHA256)
	fmt.Println("MinimumCells:", len(p.MinimumCells))
	fmt.Println("MinimumVersion:", p.MinimumVersion)
	fmt.Println("Version:", p.Version)
}
