# Public hardware names for guest hostnames

`hardware.csv` contains factual PC/model names from vendor catalogs and compact
aliases derived for hostnames. It does not contain serial numbers, service tags,
MAC addresses, user identities, driver binaries, or host inventory. The aliases
are project-defined abbreviations, not manufacturer-assigned computer names.

The 2026-11-13 snapshot contains 1,593 model records and 1,181 unique aliases:

| Vendor | Records | Source |
| --- | ---: | --- |
| Lenovo | 506 | [Commercial deployment catalog](https://download.lenovo.com/cdrt/td/catalogv2.xml) |
| Dell | 499 | [Official Driver Pack catalog](https://www.dell.com/support/kbdoc/en-us/000122176/driver-pack-catalog) |
| HP | 551 | [Official Driver Pack matrix](https://hpia.hpcloud.hp.com/downloads/driverpackcatalog/HP_Driverpack_Matrix_x64.html) |
| ASRock | 37 | [Official motherboard product page](https://www.asrock.com/mb/index.asp) |

The source names remain attributed to their respective manufacturers; no driver
software or marketing descriptions are redistributed. `hostname-sources.json`
records source URLs, input hashes, snapshot date, counts and the CSV hash.
Motherboard entries are curated in `scripts/data/hostname-boards.json`. Catalog
size reflects these sources' coverage, not current sales or market share.

## Naming

Creation chooses one of five equally likely slots:

- `DESKTOP-` followed by seven uppercase letters/digits.
- `LAPTOP-` followed by seven uppercase letters/digits.
- Two slots for a compact PC-model alias plus six or seven random letters/digits.
- One slot for a motherboard-model alias plus six or seven random letters/digits.

Hardware-style names can be uppercase or lowercase. Aliases are at most seven
characters, so the complete name stays within the portable 15-character limit.
Every choice uses `crypto/rand`; numeric/alphabet choices use rejection sampling.
The generated values are public identifiers, not authentication secrets.

This is a mixture of common default-name shapes and model-inspired custom names.
The Windows `DESKTOP-` shape is documented by
[Dell](https://www.dell.com/support/kbdoc/en-us/000137111/computer-name-shows-as-desktop-xxxxxx-on-dell-systems-that-ship-with-windows-10).
A motherboard model does not determine a machine's hostname. A sampled name is
not a claim about its SMBIOS, CPU, operating system, or actual physical hardware.
The generator is shared by Linux containers and Lima guests; it does not select
an operating-system-specific hardware profile. Existing persisted hostnames
remain valid and stable.

All data is embedded. Creation performs no network requests, inspects no host
hardware, and installs no drivers. Malformed embedded catalog data is an error.

## Updating the data

Download the three XML catalogs from the URLs recorded in the source manifest.
Dell and HP publish CAB archives: extract the XML only with a trusted archive
tool. Do not execute their driver packages. Review curated motherboard names
against their official source before changing the JSON input.

From the repository root, with local XML copies:

```sh
python3 scripts/update-hostname-catalog.py \
  --lenovo /path/to/lenovo.xml \
  --dell /path/to/DriverPackCatalog.xml \
  --hp /path/to/HPClientDriverPackCatalog.xml \
  --snapshot-date YYYY-MM-DD
```

The compiler uses only the Python standard library and makes no network calls.
It rejects XML entity/DOCTYPE declarations, limits input size, deduplicates model
records, and writes the CSV and source manifest deterministically. Check the
resulting model names and aliases, update the snapshot counts above, and run the
`guestidentity` tests. Regenerate `patches/lima/0037-random-guest-identity.patch`
when updating this bundled Lima extension.
