package wire

import "strings"

// Language tags as a server sends them (MSFP catalog section 3, core
// section 2.5): media servers often give a file's ISO 639-2 code, /B
// ("fre", "ger", "chi") or /T ("fra", "deu", "zho"); MSFP sends the ISO
// 639-1 code ("fr", "de", "zh"), with the rest of the tag kept, in BCP
// 47's case conventions ("fre-ca" is sent "fr-CA"); und, mul, zxx and
// mis are not sent.

// iso639 maps each ISO 639-2 code, /B and /T, of a language that has an
// ISO 639-1 code to that code.
var iso639 = map[string]string{
	"aar": "aa", "abk": "ab", "afr": "af", "aka": "ak", "alb": "sq", "amh": "am", "ara": "ar",
	"arg": "an", "arm": "hy", "asm": "as", "ava": "av", "ave": "ae", "aym": "ay", "aze": "az",
	"bak": "ba", "bam": "bm", "baq": "eu", "bel": "be", "ben": "bn", "bih": "bh", "bis": "bi",
	"bod": "bo", "bos": "bs", "bre": "br", "bul": "bg", "bur": "my", "cat": "ca", "ces": "cs",
	"cha": "ch", "che": "ce", "chi": "zh", "chu": "cu", "chv": "cv", "cor": "kw", "cos": "co",
	"cre": "cr", "cym": "cy", "cze": "cs", "dan": "da", "deu": "de", "div": "dv", "dut": "nl",
	"dzo": "dz", "ell": "el", "eng": "en", "epo": "eo", "est": "et", "eus": "eu", "ewe": "ee",
	"fao": "fo", "fas": "fa", "fij": "fj", "fin": "fi", "fra": "fr", "fre": "fr", "fry": "fy",
	"ful": "ff", "geo": "ka", "ger": "de", "gla": "gd", "gle": "ga", "glg": "gl", "glv": "gv",
	"gre": "el", "grn": "gn", "guj": "gu", "hat": "ht", "hau": "ha", "heb": "he", "her": "hz",
	"hin": "hi", "hmo": "ho", "hrv": "hr", "hun": "hu", "hye": "hy", "ibo": "ig", "ice": "is",
	"ido": "io", "iii": "ii", "iku": "iu", "ile": "ie", "ina": "ia", "ind": "id", "ipk": "ik",
	"isl": "is", "ita": "it", "jav": "jv", "jpn": "ja", "kal": "kl", "kan": "kn", "kas": "ks",
	"kat": "ka", "kau": "kr", "kaz": "kk", "khm": "km", "kik": "ki", "kin": "rw", "kir": "ky",
	"kom": "kv", "kon": "kg", "kor": "ko", "kua": "kj", "kur": "ku", "lao": "lo", "lat": "la",
	"lav": "lv", "lim": "li", "lin": "ln", "lit": "lt", "ltz": "lb", "lub": "lu", "lug": "lg",
	"mac": "mk", "mah": "mh", "mal": "ml", "mao": "mi", "mar": "mr", "may": "ms", "mkd": "mk",
	"mlg": "mg", "mlt": "mt", "mon": "mn", "mri": "mi", "msa": "ms", "mya": "my", "nau": "na",
	"nav": "nv", "nbl": "nr", "nde": "nd", "ndo": "ng", "nep": "ne", "nld": "nl", "nno": "nn",
	"nob": "nb", "nor": "no", "nya": "ny", "oci": "oc", "oji": "oj", "ori": "or", "orm": "om",
	"oss": "os", "pan": "pa", "per": "fa", "pli": "pi", "pol": "pl", "por": "pt", "pus": "ps",
	"que": "qu", "roh": "rm", "ron": "ro", "rum": "ro", "run": "rn", "rus": "ru", "sag": "sg",
	"san": "sa", "sin": "si", "slk": "sk", "slo": "sk", "slv": "sl", "sme": "se", "smo": "sm",
	"sna": "sn", "snd": "sd", "som": "so", "sot": "st", "spa": "es", "sqi": "sq", "srd": "sc",
	"srp": "sr", "ssw": "ss", "sun": "su", "swa": "sw", "swe": "sv", "tah": "ty", "tam": "ta",
	"tat": "tt", "tel": "te", "tgk": "tg", "tgl": "tl", "tha": "th", "tib": "bo", "tir": "ti",
	"ton": "to", "tsn": "tn", "tso": "ts", "tuk": "tk", "tur": "tr", "twi": "tw", "uig": "ug",
	"ukr": "uk", "urd": "ur", "uzb": "uz", "ven": "ve", "vie": "vi", "vol": "vo", "wel": "cy",
	"wln": "wa", "wol": "wo", "xho": "xh", "yid": "yi", "yor": "yo", "zha": "za", "zho": "zh",
	"zul": "zu",
}

// LanguageTag says whether s is a valid tag (catalog section 3): one
// already in the form a server sends, which SentTag gives back
// unchanged ("fr", "fr-CA", "yue", "zh-Hant-TW", "es-419"); "fre" (not
// yet converted), "fr-ca" or "FR" (another case), "e", "und" and "fr_CA"
// aren't. It is the one rule wherever a title or a track carries a tag,
// and for play's audioLanguage and subtitleLanguage: a receiver checks
// it and never converts the tag; an invalid one counts as absent in what
// it keeps, and refuses a request. Valid tags compare the same byte for
// byte and without case.
func LanguageTag(s string) bool {
	t, ok := SentTag(s)
	return ok && t == s
}

// tagForm says whether s has a tag's form: 2 to 12 characters of
// A–Z a–z 0–9 -.
func tagForm(s string) bool {
	return len(s) >= 2 && len(s) <= 12 && strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

// SentTag is the tag sent for code, a media server's language code
// ("fre" and "fra" give "fr", "fre-ca" "fr-CA"); false for one MSFP
// doesn't send: not of a tag's form, or und, mul, zxx or mis. Test
// cases: languageCodes in vectors/v0/strict.json.
func SentTag(code string) (string, bool) {
	if !tagForm(code) {
		return "", false
	}
	subtags := strings.Split(code, "-")
	for i, s := range subtags {
		if s == "" {
			return "", false
		}
		s = strings.ToLower(s)
		switch {
		case i == 0:
			if len(s) > 8 || !lettersOnly(s) {
				return "", false
			}
			if short, ok := iso639[s]; ok {
				s = short
			}
			if s == "und" || s == "mul" || s == "zxx" || s == "mis" {
				return "", false
			}
		case len(s) == 2 && lettersOnly(s), len(s) == 3 && strings.Trim(s, "0123456789") == "":
			s = strings.ToUpper(s)
		case len(s) == 4 && lettersOnly(s):
			s = strings.ToUpper(s[:1]) + s[1:]
		}
		subtags[i] = s
	}
	return strings.Join(subtags, "-"), true
}

// SentTags is a list of a media server's language codes as sent: each
// as SentTag gives it, those it refuses skipped, each once (compared
// without case), at most MaxTags.
func SentTags(codes []string) []string {
	var out []string
	for _, c := range codes {
		if len(out) == MaxTags {
			break
		}
		if t, ok := SentTag(c); ok && !containsFold(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func lettersOnly(s string) bool { return strings.Trim(s, "abcdefghijklmnopqrstuvwxyz") == "" }
