package outbound

import (
	"strconv"
	"strings"
)

// Wording for template parameters, in the two languages the WhatsApp
// templates are approved in. Every key exists in both; a test checks it.
// Native speakers should review the Hindi before the pilot.
var phrases = map[string]map[string]string{
	"en": {
		"fall": "they had a fall", "chest_pain": "they have chest pain", "breathing": "they are having trouble breathing",
		"fainting": "they fainted", "stroke_signs": "they may have signs of a stroke", "bleeding": "they are bleeding",
		"confusion": "they seemed confused", "self_harm": "they spoke about not wanting to live", "other": "something worrying",

		"scam:agency_threat":       "someone claiming to be police, CBI or customs",
		"scam:otp_or_bank_request": "someone asking for an OTP or bank details",
		"scam:money_request":       "someone asking for money",
		"scam:video_call_pressure": "someone pressuring them to stay on a video call",
		"scam:other":               "a suspicious caller",

		"distressed":          "they sounded very distressed in today's call",
		"missed_medicine":     "they missed a medicine on two calls in a row",
		"missed_medicine_fmt": "they did not take %s on two calls in a row",
		"said_fmt":            `in today's call they said "%s"`,

		"attempts_all": "all attempts", "attempts_one": "1 attempt", "attempts_n": "%d attempts",

		"watch:low_mood":   "They have seemed low on the last few calls; a call from you may help.",
		"watch:poor_sleep": "They have not been sleeping well for a few days.",
		"alert_note":       "We have also sent you a separate alert about today's call.",
	},
	"hi": {
		"fall": "वे गिर गए थे", "chest_pain": "उन्हें सीने में दर्द है", "breathing": "उन्हें सांस लेने में तकलीफ़ है",
		"fainting": "वे बेहोश हो गए थे", "stroke_signs": "उनमें लकवे (स्ट्रोक) के लक्षण हो सकते हैं", "bleeding": "उनका खून बह रहा है",
		"confusion": "वे उलझन में लग रहे थे", "self_harm": "उन्होंने जीने की इच्छा न होने की बात कही", "other": "कुछ चिंता की बात",

		"scam:agency_threat":       "कोई खुद को पुलिस, CBI या कस्टम्स का अफ़सर बता रहा था",
		"scam:otp_or_bank_request": "कोई OTP या बैंक की जानकारी माँग रहा था",
		"scam:money_request":       "कोई पैसे माँग रहा था",
		"scam:video_call_pressure": "कोई उन्हें वीडियो कॉल पर बने रहने को कह रहा था",
		"scam:other":               "कोई संदिग्ध कॉल करने वाला",

		"distressed":          "आज की कॉल में वे बहुत परेशान लग रहे थे",
		"missed_medicine":     "उन्होंने लगातार दो कॉल में एक दवा नहीं ली",
		"missed_medicine_fmt": "उन्होंने लगातार दो कॉल में %s नहीं ली",
		"said_fmt":            `आज की कॉल में उन्होंने कहा: "%s"`,

		"attempts_all": "सभी कोशिशें", "attempts_one": "1 कोशिश", "attempts_n": "%d कोशिशें",

		"watch:low_mood":   "पिछली कुछ कॉल में वे उदास लगे हैं; आपका एक फ़ोन उन्हें अच्छा लगेगा।",
		"watch:poor_sleep": "कुछ दिनों से उन्हें ठीक से नींद नहीं आ रही है।",
		"alert_note":       "आज की कॉल के बारे में हमने आपको एक अलग अलर्ट भी भेजा है।",
	},
}

// phrase returns the wording for key in the template language for lang,
// falling back to English.
func phrase(lang, key string) string {
	if p, ok := phrases[templateLanguage(lang)][key]; ok {
		return p
	}
	return phrases["en"][key]
}

// phrasef fills the single %s or %d in a phrase. Values are call content or
// counts, never format strings.
func phrasef(lang, key string, v any) string {
	p := phrase(lang, key)
	switch x := v.(type) {
	case int:
		return strings.Replace(p, "%d", strconv.Itoa(x), 1)
	case string:
		return strings.Replace(p, "%s", x, 1)
	}
	return p
}
