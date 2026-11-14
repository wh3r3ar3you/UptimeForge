package checks

import "strings"

// CarriesSecrets says whether running the spec needs a secret: a VPN link, a
// subscription, an MTProto secret, any catalog field marked Secret, or a URL
// with a credential in it (URLCarriesSecret). Such checks must stay off
// untrusted probes.
//
// It errs towards true: a type this binary does not know has no description of
// its fields, so any value in it may be a secret (as MaskParams assumes).
func CarriesSecrets(s *Spec) bool {
	if s == nil {
		return false
	}
	if x := s.Xray; x != nil && (x.Link != "" || x.Subscription != "" || len(x.Outbound) > 0) {
		return true
	}
	if m := s.MTProto; m != nil && (m.Link != "" || m.Secret != "") {
		return true
	}
	if h := s.HTTP; h != nil {
		// Same fields as HTTPParams.MaskSecrets, plus Headers: the catalog
		// marks them Secret and masks them separately.
		if h.Body != "" || h.BasicPassword != "" || h.BearerToken != "" || h.TLSKey != "" || len(h.Headers) > 0 {
			return true
		}
		if URLCarriesSecret(h.URL) {
			return true
		}
	}
	info, known := TypeOf(s.Type)
	for k, v := range s.Params {
		if v != "" && (!known || info.secret(k)) {
			return true
		}
		if v != "" && known && URLParam(s.Type, k) {
			for _, l := range strings.Split(v, "\n") {
				if URLCarriesSecret(l) {
					return true
				}
			}
		}
	}
	return false
}
