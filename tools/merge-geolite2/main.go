// Merge the GeoLite2 City (country + location) and ASN databases into one
// combined MaxMind .mmdb. The base tree is loaded from GeoLite2-City.mmdb so
// the result keeps the standard geoip2 schema (nested `country`, `location`,
// …); the two ASN fields are merged in at the top level, exactly where the
// geoip2 `Asn` reader expects them. Consumers can then read both country and
// ASN from a single file instead of opening three separate databases.
//
// Usage: run from the repo root with the source databases staged in upload/;
// writes upload/GeoLite2-City-ASN.mmdb.
package main

import (
	"log"
	"net"
	"os"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/inserter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	"github.com/oschwald/maxminddb-golang"
)

// Paths are relative to the repo root (the workflow stages the source
// databases in `upload/` before invoking the merge).
const (
	cityDB = "upload/GeoLite2-City.mmdb"
	asnDB  = "upload/GeoLite2-ASN.mmdb"
	outDB  = "upload/GeoLite2-City-ASN.mmdb"
)

// toInsertNetwork converts an IPv4-mapped IPv6 network (the ::ffff:0:0/96
// region — how an IPv6 mmdb iterator yields IPv4 data) back to a 4-byte IPv4
// network so mmdbwriter routes it into the canonical IPv4 subtree.
//
// IPv4 aliasing is left enabled on the base tree to keep the merged file
// compact (IPv4 data is stored once and 6to4/Teredo alias to it). Inserting the
// ::ffff: form directly would fail with "in an aliased network"; inserting the
// 4-byte form lands in the canonical subtree the aliases point at.
func toInsertNetwork(n *net.IPNet) *net.IPNet {
	ones, bits := n.Mask.Size()
	if bits == 128 && ones >= 96 {
		if ip4 := n.IP.To4(); ip4 != nil {
			return &net.IPNet{IP: ip4, Mask: net.CIDRMask(ones-96, 32)}
		}
	}
	return n
}

func main() {
	// Base tree from City. Keep IPv4 aliasing ON (default) for compactness;
	// ASN networks are converted to 4-byte before insertion (toInsertNetwork).
	tree, err := mmdbwriter.Load(cityDB, mmdbwriter.Options{
		DatabaseType: "GeoLite2-City-ASN",
	})
	if err != nil {
		log.Fatalf("load %s: %v", cityDB, err)
	}

	asn, err := maxminddb.Open(asnDB)
	if err != nil {
		log.Fatalf("open %s: %v", asnDB, err)
	}
	defer asn.Close()

	type asnRecord struct {
		Number uint32 `maxminddb:"autonomous_system_number"`
		Org    string `maxminddb:"autonomous_system_organization"`
	}

	// Skip aliased networks: insert only canonical networks. The base tree's
	// aliases (6to4/Teredo → IPv4) keep resolving to the merged data.
	networks := asn.Networks(maxminddb.SkipAliasedNetworks)
	var merged, skipped int
	for networks.Next() {
		var rec asnRecord
		subnet, err := networks.Network(&rec)
		if err != nil {
			log.Fatalf("read asn network: %v", err)
		}
		data := mmdbtype.Map{
			"autonomous_system_number":       mmdbtype.Uint32(rec.Number),
			"autonomous_system_organization": mmdbtype.String(rec.Org),
		}
		if err := tree.InsertFunc(toInsertNetwork(subnet), inserter.TopLevelMergeWith(data)); err != nil {
			// Don't fail the whole build on an odd network (e.g. a reserved
			// range); log and continue so the merge stays robust.
			log.Printf("skip asn network %s: %v", subnet, err)
			skipped++
			continue
		}
		merged++
	}
	if err := networks.Err(); err != nil {
		log.Fatalf("iterate asn networks: %v", err)
	}

	if err := os.MkdirAll("upload", 0o755); err != nil {
		log.Fatalf("mkdir upload: %v", err)
	}
	out, err := os.Create(outDB)
	if err != nil {
		log.Fatalf("create %s: %v", outDB, err)
	}
	defer out.Close()
	if _, err := tree.WriteTo(out); err != nil {
		log.Fatalf("write %s: %v", outDB, err)
	}
	log.Printf("merged %d ASN networks (%d skipped); wrote %s", merged, skipped, outDB)
}
