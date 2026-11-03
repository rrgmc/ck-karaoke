package kmapi

// languageCodes maps the unit's English language names (TableLan.LanName_EG)
// to ISO 639-1 codes, which KaraokeMachine sends. A name with no code, such as
// a genre the unit files as a language, is sent as null.
var languageCodes = map[string]string{
	"Chinese": "zh", "English": "en", "Korean": "ko", "Vietnamese": "vi", "Thai": "th",
	"Brazil": "pt", "Spanish": "es", "Tagalog": "tl", "Italian": "it", "German": "de",
	"French": "fr", "Portuguese": "pt", "Indonesian": "id", "Japanese": "ja", "Russian": "ru",
	"Holland": "nl", "Hindi": "hi", "Persian": "fa", "Hebrew": "he", "Polish": "pl",
	"Malaysia": "ms", "Khmer": "km", "Mongolian": "mn", "Arabic": "ar", "Tuikish": "tr",
	"Gujarati": "gu", "Malayalam": "ml", "Marathi": "mr", "Tamil": "ta", "Telugu": "te",
	"Bengali": "bn", "Kannada": "kn", "Sinhala": "si", "Urdu": "ur", "Punjabi": "pa",
	"Nepali": "ne", "Dhivehi": "dv", "Kazakh": "kk", "Inner Mongolian": "mn", "Serbian": "sr",
	"Danish": "da", "Finnish": "fi", "Croation": "hr", "Swedish": "sv", "Odia": "or",
}

// languageCode returns the ISO 639-1 code for a unit language name, or nil.
func languageCode(name string) *string {
	if c, ok := languageCodes[name]; ok {
		return &c
	}
	return nil
}
