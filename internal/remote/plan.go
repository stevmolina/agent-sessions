package remote

import "errors"

// Row is one cleaned session ready to upload.
type Row struct {
	Source, ID, Revision, Fingerprint string
}

// CheckFingerprint refuses an index that was not cleaned with Doppler.
func CheckFingerprint(fingerprint string) error {
	switch {
	case fingerprint == "":
		return errors.New("refusing to push: index has no clean fingerprint")
	case len(fingerprint) >= len("v1-override:") && fingerprint[:len("v1-override:")] == "v1-override:":
		return errors.New("refusing to push: SESSIONS_SECRETS_JSON is set")
	default:
		return nil
	}
}

type plan struct {
	upsert []Row
	remove []Row
}

func planSync(local, remote []Row, allowDelete bool) plan {
	have := map[string]Row{}
	for _, row := range remote {
		have[key(row)] = row
	}
	var out plan
	seen := map[string]bool{}
	for _, row := range local {
		seen[key(row)] = true
		prev, ok := have[key(row)]
		if !ok || prev.Revision != row.Revision || prev.Fingerprint != row.Fingerprint {
			out.upsert = append(out.upsert, row)
		}
	}
	if allowDelete {
		for _, row := range remote {
			if !seen[key(row)] {
				out.remove = append(out.remove, row)
			}
		}
	}
	return out
}

func key(row Row) string { return row.Source + "\x00" + row.ID }
