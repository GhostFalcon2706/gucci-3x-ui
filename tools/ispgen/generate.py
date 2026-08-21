#!/usr/bin/env python3
"""Regenerate internal/isp/data/catalog.json.

The catalog powers the per-client "allowed networks" lock: it maps every
selectable access network (Iranian mobile operator, fixed/wireless ISP, plus the
global satellite providers) to the IP prefixes it announces.

Data sources, all public:

* https://ftp.ripe.net/ripe/asnames/asn.txt  — AS number -> organisation, country
* https://raw.githubusercontent.com/ipverse/asn-ip/master/as/<asn>/aggregated.json
  — the prefixes each AS announces, already aggregated

Every Iranian organisation that announces at least MIN_PREFIXES routes is
included, so the list covers the whole country rather than a hand-picked few.
Well-known operators additionally get a Persian display name and a category via
CURATED below.

Usage:  python3 tools/ispgen/generate.py [output.json]
"""

from __future__ import annotations

import concurrent.futures
import ipaddress
import json
import pathlib
import re
import sys
import urllib.request

ASN_TXT = "https://ftp.ripe.net/ripe/asnames/asn.txt"
IPVERSE = "https://raw.githubusercontent.com/ipverse/asn-ip/master/as/{asn}/aggregated.json"

# An organisation must announce at least this many aggregated prefixes to be
# offered as a selectable network. Below that the entry is almost always a
# single company's office range, not an access network someone browses from.
MIN_PREFIXES = 6

# id -> (Persian name, English name, kind, regex matched against the RIPE
# organisation name). Order here is the order shown in the UI.
CURATED: list[tuple[str, str, str, str, str]] = [
    # ---- mobile network operators & MVNOs -------------------------------
    ("mci", "همراه اول", "Hamrah-e Aval (MCI)", "mobile", r"mobile communication company of iran"),
    ("irancell", "ایرانسل", "Irancell (MTN)", "mobile", r"iran cell service"),
    ("rightel", "رایتل", "RighTel", "mobile", r"rightel"),
    ("shatelmobile", "شاتل موبایل", "Shatel Mobile", "mobile", r"^$"),  # AS34369, pinned below
    ("samantel", "سامانتل", "SamanTel", "mobile", r"kish cell pars"),
    ("taliya", "تالیا", "Taliya", "mobile", r"taliya"),
    ("ariantel", "آریانتل", "ArianTel", "mobile", r"arian tel|ariantel"),
    ("lotustel", "لوتوس‌تل (آپتل)", "LotusTel / ApTel", "mobile", r"parsian hamrah lotus|lotus net"),
    # ---- fixed line: ADSL / VDSL / FTTH ---------------------------------
    ("tci", "مخابرات ایران", "TCI / Mokhaberat", "fixed", r"iran telecommunication company|telecommunication infrastructure company|iran information technology company"),
    ("shatel", "شاتل", "Shatel", "fixed", r"aria shatel"),
    ("asiatech", "آسیاتک", "Asiatech", "fixed", r"asiatech|asre dadeha asiatech"),
    ("parsonline", "پارس‌آنلاین", "ParsOnline", "fixed", r"parsan lin"),
    ("hiweb", "های‌وب", "HiWEB", "fixed", r"dadeh gostar asr novin"),
    ("pishgaman", "پیشگامان", "Pishgaman", "fixed", r"pishgaman"),
    ("fanava", "فن‌آوا", "Fanava", "fixed", r"fanava group|dade samane fanava"),
    ("respina", "رسپینا", "Respina", "fixed", r"respina"),
    ("sabanet", "صبانت", "SabaNet", "fixed", r"neda gostar saba|parvaresh dadeha"),
    ("datak", "داتک", "Datak", "fixed", r"datak company"),
    ("fanaptelecom", "فناپ تلکام", "FANAP Telecom", "fixed", r"pasargad arian|pasargad aryan"),
    ("afranet", "افرانت", "Afranet", "fixed", r"^afranet$"),
    ("askhazar", "اندیشه سبز خزر", "Andishe Sabz Khazar", "fixed", r"andishe sabz khazar"),
    ("rdg", "رایانه دانش گلستان", "Rayaneh Danesh Golestan", "fixed", r"rayaneh danesh golestan"),
    ("sepanta", "سپنتا", "Sepanta", "fixed", r"sepanta communication"),
    ("farahoosh", "فراهوش دنا", "Farahoosh Dena", "fixed", r"farahoosh dena"),
    ("sefroyek", "صفر و یک پرداز", "Sefroyek Pardaz", "fixed", r"sefroyek pardaz"),
    ("rasa", "افرا ارتباطات رسا", "Afra Ertebatat Rasa", "fixed", r"afra ertebatat"),
    ("mehregan", "شبکه سبز مهرگان", "Shabakeh Sabz Mehregan", "fixed", r"shabakeh sabz mehregan"),
    ("atrin", "آترین", "Atrin ICT", "fixed", r"atrin information"),
    # ---- wireless / TD-LTE ----------------------------------------------
    ("mobinnet", "مبین‌نت", "Mobinnet", "wireless", r"mobin net communication"),
    # ---- satellite -------------------------------------------------------
    ("starlink", "استارلینک", "Starlink (SpaceX)", "satellite", r"^$"),
    ("oneweb", "وان‌وب", "OneWeb", "satellite", r"^$"),
    ("viasat", "وایاست", "Viasat", "satellite", r"^$"),
    ("hughesnet", "هیوزنت", "HughesNet", "satellite", r"^$"),
    ("eutelsat", "یوتلست", "Eutelsat", "satellite", r"^$"),
    ("marlink", "مارلینک", "Marlink (VSAT)", "satellite", r"^$"),
    ("iridium", "ایریدیوم", "Iridium", "satellite", r"^$"),
]

# ASNs pinned to an id regardless of the organisation name (shared holdings, or
# non-Iranian operators that are not part of the country sweep).
PINNED: dict[str, list[int]] = {
    "shatelmobile": [34369],
    "starlink": [14593],
    "oneweb": [800],
    "viasat": [7058, 7155, 7168, 16491],
    "hughesnet": [1358, 6621, 12440],
    "eutelsat": [15829],
    "marlink": [5377, 8264, 9207, 14549, 22218, 24039],
    "iridium": [16255, 22184],
}

# Kept out of an id even if the name pattern matches (e.g. Shatel Mobile must
# not be folded into Shatel's fixed-line entry).
EXCLUDED: dict[str, set[int]] = {"shatel": {34369}}


def fetch_text(url: str) -> str:
    with urllib.request.urlopen(url, timeout=120) as response:
        return response.read().decode("utf-8", "replace")


def load_asn_table() -> list[tuple[int, str, str]]:
    rows = []
    for line in fetch_text(ASN_TXT).splitlines():
        match = re.match(r"^(\d+)\s+(\S+)\s*(.*), ([A-Z]{2})$", line.strip())
        if not match:
            continue
        asn, handle, org, country = int(match.group(1)), match.group(2), match.group(3).strip().strip('"'), match.group(4)
        rows.append((asn, org or handle, country))
    return rows


def fetch_prefixes(asn: int) -> tuple[int, list[str], list[str]]:
    for _ in range(2):
        try:
            with urllib.request.urlopen(IPVERSE.format(asn=asn), timeout=30) as response:
                payload = json.load(response)
            prefixes = payload.get("prefixes", {})
            return asn, prefixes.get("ipv4", []), prefixes.get("ipv6", [])
        except Exception:  # noqa: BLE001 - offline ASNs are expected
            continue
    return asn, [], []


def collapse(networks: list[str]) -> list[str]:
    parsed = []
    for net in networks:
        try:
            parsed.append(ipaddress.ip_network(net, strict=False))
        except ValueError:
            continue
    return [str(n) for n in ipaddress.collapse_addresses(parsed)]


def slugify(name: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", name.lower()).strip("-")
    return re.sub(r"-(co|ltd|llc|pjs|pjsc|inc|company|corporation)$", "", slug)[:40] or "network"


def main() -> None:
    out_path = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "internal/isp/data/catalog.json")

    table = load_asn_table()
    iran = [(asn, org) for asn, org, country in table if country == "IR"]
    wanted = {asn for asn, _ in iran} | {asn for asns in PINNED.values() for asn in asns}

    prefixes: dict[int, tuple[list[str], list[str]]] = {}
    with concurrent.futures.ThreadPoolExecutor(16) as pool:
        for asn, v4, v6 in pool.map(fetch_prefixes, sorted(wanted)):
            prefixes[asn] = (v4, v6)

    assigned: set[int] = set()
    entries = []

    for entry_id, name_fa, name_en, kind, pattern in CURATED:
        asns = set(PINNED.get(entry_id, []))
        compiled = re.compile(pattern, re.IGNORECASE)
        for asn, org in iran:
            if asn in EXCLUDED.get(entry_id, set()):
                continue
            if compiled.search(org):
                asns.add(asn)
        asns -= (assigned - set(PINNED.get(entry_id, [])))
        v4, v6 = [], []
        for asn in asns:
            v4 += prefixes.get(asn, ([], []))[0]
            v6 += prefixes.get(asn, ([], []))[1]
        assigned |= asns
        entries.append({
            "id": entry_id,
            "nameFa": name_fa,
            "nameEn": name_en,
            "kind": kind,
            "asns": sorted(asns),
            "v4": collapse(v4),
            "v6": collapse(v6),
        })

    # Everything else registered in Iran that actually routes traffic, so the
    # list covers the whole country and not only the household names.
    by_org: dict[str, list[int]] = {}
    for asn, org in iran:
        if asn in assigned:
            continue
        by_org.setdefault(org, []).append(asn)

    others = []
    used_ids = {e["id"] for e in entries}
    for org, asns in by_org.items():
        v4, v6 = [], []
        for asn in asns:
            v4 += prefixes.get(asn, ([], []))[0]
            v6 += prefixes.get(asn, ([], []))[1]
        v4, v6 = collapse(v4), collapse(v6)
        if len(v4) + len(v6) < MIN_PREFIXES:
            continue
        entry_id = slugify(org)
        suffix = 2
        while entry_id in used_ids:
            entry_id = f"{slugify(org)}-{suffix}"
            suffix += 1
        used_ids.add(entry_id)
        others.append({
            "id": entry_id,
            "nameFa": org,
            "nameEn": org,
            "kind": "other",
            "asns": sorted(asns),
            "v4": v4,
            "v6": v6,
        })
    others.sort(key=lambda e: -(len(e["v4"]) + len(e["v6"])))
    entries.extend(others)

    payload = {
        "source": "RIPE NCC asn.txt + ipverse/asn-ip",
        "minPrefixes": MIN_PREFIXES,
        "isps": entries,
    }
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=False))

    total = sum(len(e["v4"]) + len(e["v6"]) for e in entries)
    print(f"wrote {out_path}: {len(entries)} networks, {total} prefixes, {out_path.stat().st_size} bytes")


if __name__ == "__main__":
    main()
