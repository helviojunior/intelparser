package tools

//Source https://github.com/dustin/go-humanize/blob/master/bytes.go

import (
    "fmt"
    "math"
    "strconv"
    "strings"
    "unicode"
    "crypto/sha1"
    "encoding/hex"
    "time"
)

// IEC Sizes.
// kibis of bits
const (
    Byte = 1 << (iota * 10)
    KiByte
    MiByte
    GiByte
    TiByte
    PiByte
    EiByte
)

// SI Sizes.
const (
    IByte = 1
    KByte = IByte * 1000
    MByte = KByte * 1000
    GByte = MByte * 1000
    TByte = GByte * 1000
    PByte = TByte * 1000
    EByte = PByte * 1000
)

var bytesSizeTable = map[string]uint64{
    "b":   Byte,
    "kib": KiByte,
    "kb":  KByte,
    "mib": MiByte,
    "mb":  MByte,
    "gib": GiByte,
    "gb":  GByte,
    "tib": TiByte,
    "tb":  TByte,
    "pib": PiByte,
    "pb":  PByte,
    "eib": EiByte,
    "eb":  EByte,
    // Without suffix
    "":   Byte,
    "ki": KiByte,
    "k":  KByte,
    "mi": MiByte,
    "m":  MByte,
    "gi": GiByte,
    "g":  GByte,
    "ti": TiByte,
    "t":  TByte,
    "pi": PiByte,
    "p":  PByte,
    "ei": EiByte,
    "e":  EByte,
}

func logn(n, b float64) float64 {
    return math.Log(n) / math.Log(b)
}

func HumanateBytes(s uint64, base float64, sizes []string) string {
    if s < 10 {
        return fmt.Sprintf("%d B", s)
    }
    l := float64(len(sizes))
    e := math.Floor(logn(float64(s), base))
    if e >= l {
        e = l - 1.0
    }
    suffix := sizes[int(e)]
    val := math.Floor(float64(s)/math.Pow(base, e)*10+0.5) / 10
    if val > 1000 {
        return fmt.Sprintf("%s %s", FormatInt64Comma(int64(val)), suffix)
    }else {
        f := "%.0f %s"
        if val < 10 {
            f = "%.1f %s"
        }
        return fmt.Sprintf(f, val, suffix)
    }

}


// Bytes produces a human readable representation of an SI size.
//
// See also: ParseBytes.
//
// Bytes(82854982) -> 83 MB
func Bytes(s uint64) string {
    sizes := []string{"B", "kB", "MB", "GB", "TB", "PB", "EB"}
    return HumanateBytes(s, 1000, sizes)
}

// IBytes produces a human readable representation of an IEC size.
//
// See also: ParseBytes.
//
// IBytes(82854982) -> 79 MiB
func IBytes(s uint64) string {
    sizes := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
    return HumanateBytes(s, 1024, sizes)
}

// ParseBytes parses a string representation of bytes into the number
// of bytes it represents.
//
// See Also: Bytes, IBytes.
//
// ParseBytes("42 MB") -> 42000000, nil
// ParseBytes("42 mib") -> 44040192, nil
func ParseBytes(s string) (uint64, error) {
    lastDigit := 0
    hasComma := false
    for _, r := range s {
        if !(unicode.IsDigit(r) || r == '.' || r == ',') {
            break
        }
        if r == ',' {
            hasComma = true
        }
        lastDigit++
    }

    num := s[:lastDigit]
    if hasComma {
        num = strings.Replace(num, ",", "", -1)
    }

    f, err := strconv.ParseFloat(num, 64)
    if err != nil {
        return 0, err
    }

    extra := strings.ToLower(strings.TrimSpace(s[lastDigit:]))
    if m, ok := bytesSizeTable[extra]; ok {
        f *= float64(m)
        if f >= math.MaxUint64 {
            return 0, fmt.Errorf("too large: %v", s)
        }
        return uint64(f), nil
    }

    return 0, fmt.Errorf("unhandled size name: %v", extra)
}


func GetHash(data []byte) string {
    h := sha1.New()
    h.Write(data)
    return hex.EncodeToString(h.Sum(nil))
}

func GetHashFromValues(values ...interface{}) string {

    data := ""
    for _, v := range values {
        if _, ok := v.(int); ok {
            data += fmt.Sprintf("%d,", v)
        }else if dt, ok := v.(time.Time); ok {
            data += dt.Format(time.RFC3339)
        }else{
            data += fmt.Sprintf("%s,", v)
        }
    }

    h := sha1.New()
    h.Write([]byte(data))

    return hex.EncodeToString(h.Sum(nil))

}
