//go:build linux && cgo

package idmap

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIdmapSetAddSafe_split(t *testing.T) {
	orig := IdmapSet{Idmap: []IdmapEntry{{Isuid: true, Hostid: 1000, Nsid: 0, Maprange: 1000}}}

	err := orig.AddSafe(IdmapEntry{Isuid: true, Hostid: 500, Nsid: 500, Maprange: 10})
	if err != nil {
		t.Error(err)
		return
	}

	if orig.Idmap[0].Hostid != 1000 || orig.Idmap[0].Nsid != 0 || orig.Idmap[0].Maprange != 500 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[0]))
		return
	}

	if orig.Idmap[1].Hostid != 500 || orig.Idmap[1].Nsid != 500 || orig.Idmap[1].Maprange != 10 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[1]))
		return
	}

	if orig.Idmap[2].Hostid != 1510 || orig.Idmap[2].Nsid != 510 || orig.Idmap[2].Maprange != 490 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[2]))
		return
	}

	if len(orig.Idmap) != 3 {
		t.Error("too many idmap entries")
		return
	}
}

func TestIdmapSetAddSafe_lower(t *testing.T) {
	orig := IdmapSet{Idmap: []IdmapEntry{{Isuid: true, Hostid: 1000, Nsid: 0, Maprange: 1000}}}

	err := orig.AddSafe(IdmapEntry{Isuid: true, Hostid: 500, Nsid: 0, Maprange: 10})
	if err != nil {
		t.Error(err)
		return
	}

	if orig.Idmap[0].Hostid != 500 || orig.Idmap[0].Nsid != 0 || orig.Idmap[0].Maprange != 10 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[0]))
		return
	}

	if orig.Idmap[1].Hostid != 1010 || orig.Idmap[1].Nsid != 10 || orig.Idmap[1].Maprange != 990 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[1]))
		return
	}

	if len(orig.Idmap) != 2 {
		t.Error("too many idmap entries")
		return
	}
}

func TestIdmapSetAddSafe_upper(t *testing.T) {
	orig := IdmapSet{Idmap: []IdmapEntry{{Isuid: true, Hostid: 1000, Nsid: 0, Maprange: 1000}}}

	err := orig.AddSafe(IdmapEntry{Isuid: true, Hostid: 500, Nsid: 995, Maprange: 10})
	if err != nil {
		t.Error(err)
		return
	}

	if orig.Idmap[0].Hostid != 1000 || orig.Idmap[0].Nsid != 0 || orig.Idmap[0].Maprange != 995 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[0]))
		return
	}

	if orig.Idmap[1].Hostid != 500 || orig.Idmap[1].Nsid != 995 || orig.Idmap[1].Maprange != 10 {
		t.Error(fmt.Errorf("bad range: %v", orig.Idmap[1]))
		return
	}

	if len(orig.Idmap) != 2 {
		t.Error("too many idmap entries")
		return
	}
}

func TestIdmapSetIntersects(t *testing.T) {
	orig := IdmapSet{Idmap: []IdmapEntry{{Isuid: true, Hostid: 165536, Nsid: 0, Maprange: 65536}}}

	if !orig.Intersects(IdmapEntry{Isuid: true, Hostid: 231071, Nsid: 0, Maprange: 65536}) {
		t.Error("ranges don't intersect")
		return
	}

	if !orig.Intersects(IdmapEntry{Isuid: true, Hostid: 231072, Nsid: 0, Maprange: 65536}) {
		t.Error("ranges don't intersect")
		return
	}

	if !orig.Intersects(IdmapEntry{Isuid: true, Hostid: 231072, Nsid: 65535, Maprange: 65536}) {
		t.Error("ranges don't intersect")
		return
	}

	if orig.Intersects(IdmapEntry{Isuid: true, Hostid: 231072, Nsid: 65536, Maprange: 65536}) {
		t.Error("ranges intersect")
		return
	}
}

func TestIdmapHostIDMapRange(t *testing.T) {
	// Check empty entry is not covered.
	idmap := IdmapEntry{}
	assert.False(t, idmap.HostIDsCoveredBy(nil, nil))

	// Check nil allowed lists are not covered.
	idmap = IdmapEntry{Isuid: true, Hostid: 1000, Maprange: 1}
	assert.False(t, idmap.HostIDsCoveredBy(nil, nil))

	// Check that UID/GID specific host IDs are covered by equivalent UID/GID specific host ID rule.
	uidOnlyEntry := IdmapEntry{Isuid: true, Hostid: 1000, Maprange: 1}
	gidOnlyEntry := IdmapEntry{Isgid: true, Hostid: 1000, Maprange: 1}

	allowedUIDMaps := []IdmapEntry{
		{Isuid: true, Hostid: 1000, Maprange: 1},
	}

	allowedGIDMaps := []IdmapEntry{
		{Isgid: true, Hostid: 1000, Maprange: 1},
	}

	assert.True(t, uidOnlyEntry.HostIDsCoveredBy(allowedUIDMaps, nil))
	assert.False(t, uidOnlyEntry.HostIDsCoveredBy(nil, allowedUIDMaps))
	assert.True(t, uidOnlyEntry.HostIDsCoveredBy(allowedUIDMaps, allowedUIDMaps))

	assert.False(t, uidOnlyEntry.HostIDsCoveredBy(allowedGIDMaps, nil))
	assert.False(t, uidOnlyEntry.HostIDsCoveredBy(nil, allowedGIDMaps))
	assert.False(t, uidOnlyEntry.HostIDsCoveredBy(allowedGIDMaps, allowedGIDMaps))

	assert.False(t, gidOnlyEntry.HostIDsCoveredBy(allowedGIDMaps, nil))
	assert.True(t, gidOnlyEntry.HostIDsCoveredBy(nil, allowedGIDMaps))
	assert.True(t, gidOnlyEntry.HostIDsCoveredBy(allowedGIDMaps, allowedGIDMaps))

	assert.False(t, gidOnlyEntry.HostIDsCoveredBy(allowedUIDMaps, nil))
	assert.False(t, gidOnlyEntry.HostIDsCoveredBy(nil, allowedUIDMaps))
	assert.False(t, gidOnlyEntry.HostIDsCoveredBy(allowedUIDMaps, allowedUIDMaps))

	// Check ranges are correctly blocked when not covered by single ID allow list.
	uidOnlyRangeEntry := IdmapEntry{Isuid: true, Hostid: 1000, Maprange: 2}
	gidOnlyRangeEntry := IdmapEntry{Isgid: true, Hostid: 1000, Maprange: 2}

	assert.False(t, uidOnlyRangeEntry.HostIDsCoveredBy(allowedUIDMaps, nil))
	assert.False(t, uidOnlyRangeEntry.HostIDsCoveredBy(nil, allowedUIDMaps))
	assert.False(t, uidOnlyRangeEntry.HostIDsCoveredBy(allowedUIDMaps, allowedUIDMaps))

	assert.False(t, gidOnlyRangeEntry.HostIDsCoveredBy(allowedGIDMaps, nil))
	assert.False(t, gidOnlyRangeEntry.HostIDsCoveredBy(nil, allowedGIDMaps))
	assert.False(t, gidOnlyRangeEntry.HostIDsCoveredBy(allowedGIDMaps, allowedGIDMaps))

	// Check ranges are allowed when fully covered.
	allowedUIDMaps = []IdmapEntry{
		{Isuid: true, Hostid: 1000, Maprange: 2},
	}

	allowedGIDMaps = []IdmapEntry{
		{Isgid: true, Hostid: 1000, Maprange: 2},
	}

	assert.True(t, uidOnlyRangeEntry.HostIDsCoveredBy(allowedUIDMaps, nil))
	assert.False(t, uidOnlyRangeEntry.HostIDsCoveredBy(nil, allowedUIDMaps))
	assert.True(t, uidOnlyRangeEntry.HostIDsCoveredBy(allowedUIDMaps, allowedUIDMaps))

	assert.False(t, gidOnlyRangeEntry.HostIDsCoveredBy(allowedGIDMaps, nil))
	assert.True(t, gidOnlyRangeEntry.HostIDsCoveredBy(nil, allowedGIDMaps))
	assert.True(t, gidOnlyRangeEntry.HostIDsCoveredBy(allowedGIDMaps, allowedGIDMaps))

	// Check ranges for combined allowed ID maps are correctly validated.
	allowedCombinedMaps := []IdmapEntry{
		{Isuid: true, Isgid: true, Hostid: 1000, Maprange: 2},
	}

	assert.True(t, uidOnlyRangeEntry.HostIDsCoveredBy(allowedCombinedMaps, nil))
	assert.False(t, uidOnlyRangeEntry.HostIDsCoveredBy(nil, allowedCombinedMaps))
	assert.True(t, uidOnlyRangeEntry.HostIDsCoveredBy(allowedCombinedMaps, allowedCombinedMaps))

	assert.False(t, gidOnlyRangeEntry.HostIDsCoveredBy(allowedCombinedMaps, nil))
	assert.True(t, gidOnlyRangeEntry.HostIDsCoveredBy(nil, allowedCombinedMaps))
	assert.True(t, gidOnlyRangeEntry.HostIDsCoveredBy(allowedCombinedMaps, allowedCombinedMaps))

	combinedEntry := IdmapEntry{Isuid: true, Isgid: true, Hostid: 1000, Maprange: 1}

	assert.False(t, combinedEntry.HostIDsCoveredBy(allowedCombinedMaps, nil))
	assert.False(t, combinedEntry.HostIDsCoveredBy(nil, allowedCombinedMaps))
	assert.True(t, combinedEntry.HostIDsCoveredBy(allowedCombinedMaps, allowedCombinedMaps))

	assert.False(t, combinedEntry.HostIDsCoveredBy(allowedCombinedMaps, nil))
	assert.False(t, combinedEntry.HostIDsCoveredBy(nil, allowedCombinedMaps))
	assert.True(t, combinedEntry.HostIDsCoveredBy(allowedCombinedMaps, allowedCombinedMaps))
}
