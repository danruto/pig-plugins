package usage

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var consumerISODate = regexp.MustCompile(`^([+-][0-9]{6}|[0-9]{4})(?:-([0-9]{2})(?:-([0-9]{2}))?)?(?:[Tt]([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]+))?)?([Zz]|[+-][0-9]{2}:?[0-9]{2})?)?$`)
var consumerLegacyDate = regexp.MustCompile(`^[\x00-\x20\x{00a0}]*([+-][0-9]{6}|[0-9]{4})(?:-([0-9]{1,2})(?:-([0-9]{1,2}))?)?(?:[\x00-\x20\x{00a0}]+([0-9]{1,2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]+))?)?([Zz]|[+-][0-9]{2}:?[0-9]{2})?)?[\x00-\x20\x{00a0}]*$`)

var consumerLegacyWhitespace = regexp.MustCompile(`[\x01-\x20\x{00a0}]+`)

func consumerDateSeconds(text string) *float64 {
	if terminator := strings.IndexByte(text, 0); terminator >= 0 {
		text = text[:terminator]
	}
	match := consumerISODate.FindStringSubmatch(text)
	legacy := match == nil
	if legacy {
		match = consumerLegacyDate.FindStringSubmatch(text)
		if match == nil {
			return consumerLegacyRFCDate(text)
		}
	}
	negativeZeroYear := match[1] == "-000000"
	if negativeZeroYear {
		if strings.ContainsAny(text, "Tt") || match[2] == "" {
			return nil
		}
		legacy = true
	}
	parts := []int{0, 1, 1, 0, 0, 0}
	for i := range parts {
		if match[i+1] != "" {
			value, err := strconv.Atoi(match[i+1])
			if err != nil {
				return nil
			}
			parts[i] = value
		}
	}
	year, month, day, hour, minute, second := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
	if legacy && year > 0 && year <= 31 {
		year, month, day = day, year, month
		if match[3] == "" {
			year = 2001
		}
		if match[2] == "" {
			day = 1
		}
	}
	if negativeZeroYear {
		year = 2001
	}
	if legacy && year >= 0 && year < 100 {
		if year < 50 {
			year += 2000
		} else {
			year += 1900
		}
	}
	fraction := match[7]
	millis := 0
	if fraction != "" {
		fraction = (fraction + "000")[:3]
		value, err := strconv.Atoi(fraction)
		if err != nil {
			return nil
		}
		millis = value
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 24 || minute > 59 || second > 59 || hour == 24 && (minute != 0 || second != 0 || strings.Trim(match[7], "0") != "") {
		return nil
	}
	location := time.UTC
	if (legacy || match[4] != "") && match[8] == "" {
		location = time.Local
	}
	zone := match[8]
	if zone != "" && zone != "Z" && zone != "z" {
		digits := strings.ReplaceAll(zone[1:], ":", "")
		hours, err := strconv.Atoi(digits[:2])
		if err != nil {
			return nil
		}
		minutes, err := strconv.Atoi(digits[2:])
		if err != nil {
			return nil
		}
		if !legacy && hours > 23 || minutes > 59 {
			return nil
		}
		offset := (hours*60 + minutes) * 60
		if zone[0] == '-' {
			offset = -offset
		}
		location = time.FixedZone("", offset)
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, millis*1000000, location)
	milliseconds := float64(parsed.UnixMilli())
	if math.Abs(milliseconds) > 8640000000000000 {
		return nil
	}
	return new(math.Floor(milliseconds / 1000))
}

func consumerLegacyRFCDate(text string) *float64 {
	text = strings.Trim(consumerLegacyWhitespace.ReplaceAllString(text, " "), " ")
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, "01/02/2006", "January 2, 2006 15:04:05 GMT"} {
		location := time.UTC
		if layout == "01/02/2006" {
			location = time.Local
		}
		if strings.HasSuffix(layout, " MST") {
			zone := text[strings.LastIndexByte(text, ' ')+1:]
			offset, known := map[string]int{"UTC": 0, "GMT": 0, "EST": -5 * 3600, "EDT": -4 * 3600, "CST": -6 * 3600, "CDT": -5 * 3600, "MST": -7 * 3600, "MDT": -6 * 3600, "PST": -8 * 3600, "PDT": -7 * 3600}[zone]
			if !known && (strings.HasPrefix(zone, "GMT+") || strings.HasPrefix(zone, "GMT-")) {
				numeric := zone[3:]
				if len(numeric) == 2 || len(numeric) == 4 {
					numeric = numeric[:1] + "0" + numeric[1:]
				}
				for _, zoneLayout := range []string{"-07", "-0700"} {
					if parsed, err := time.Parse(zoneLayout, numeric); err == nil {
						_, offset = parsed.Zone()
						known = true
						break
					}
				}
			}
			if !known {
				continue
			}
			location = time.FixedZone(zone, offset)
		}
		if parsed, err := time.ParseInLocation(layout, text, location); err == nil {
			return new(math.Floor(float64(parsed.UnixMilli()) / 1000))
		}
	}
	return nil
}
