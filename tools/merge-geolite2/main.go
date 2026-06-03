// Merge the GeoLite2 City (country + location) and ASN databases into one
// combined MaxMind .mmdb. The base tree is loaded from GeoLite2-City.mmdb so
// the result keeps the standard geoip2 schema (nested `country`, `location`,
// …); the two ASN fields are merged in at the top level, exactly where the
// geoip2 `Asn` reader expects them. Consumers can then read both country and
// ASN from a single file instead of opening three separate databases.
//
// Usage: run from a directory containing GeoLite2-City.mmdb and
// GeoLite2-ASN.mmdb; writes upload/GeoLite2-City-ASN.mmdb.
package main

import (
	"log"
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

func main() {
	// Load City as the writable base. Keep a distinct database_type so the
	// merged file is identifiable while staying geoip2-schema compatible.
	//
	// DisableIPv4Aliasing: the GeoLite2 databases store IPv4 data via IPv6
	// aliasing (6to4 / Teredo nodes). Loading with aliasing on turns those into
	// alias nodes, and then inserting an IPv4 ASN network (mapped to ::ffff:.../
	// 120) fails with "in an aliased network". Disabling aliasing loads the
	// IPv4 data as plain records under ::ffff:0:0/96, so IPv4 lookups still
	// resolve (readers map IPv4 → ::ffff:) and ASN inserts succeed.
	// IncludeReservedNetworks: avoid "reserved network" insert errors if the
	// ASN database carries any reserved ranges.
	tree, err := mmdbwriter.Load(cityDB, mmdbwriter.Options{
		DatabaseType:            "GeoLite2-City-ASN",
		DisableIPv4Aliasing:     true,
		IncludeReservedNetworks: true,
	})
	if err != nil {
		log.Fatalf("load %s: %v", cityDB, err)
	}

	asn, err := maxminddb.Open(asnDB)
	if err != nil {
		log.Fatalf("open %s: %v", asnDB, err)
	}
	defer asn.Close()

	var rec struct {
		Number uint32 `maxminddb:"autonomous_system_number"`
		Org    string `maxminddb:"autonomous_system_organization"`
	}

	networks := asn.Networks()
	merged := 0
	for networks.Next() {
		rec = struct {
			Number uint32 `maxminddb:"autonomous_system_number"`
			Org    string `maxminddb:"autonomous_system_organization"`
		}{}
		subnet, err := networks.Network(&rec)
		if err != nil {
			log.Fatalf("read asn network: %v", err)
		}
		data := mmdbtype.Map{
			"autonomous_system_number":       mmdbtype.Uint32(rec.Number),
			"autonomous_system_organization": mmdbtype.String(rec.Org),
		}
		if err := tree.InsertFunc(subnet, inserter.TopLevelMergeWith(data)); err != nil {
			log.Fatalf("merge asn into %s: %v", subnet, err)
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
	log.Printf("merged %d ASN networks; wrote %s", merged, outDB)
}
