package botruntime

import "github.com/killerprojecte/HumanEngine/backend/internal/dispatch"

// Mirrors the legacy P1-B standalone media-marker rule, not a ban on ordinary
// discussions of memes. Only text parts following an actual search are checked.
var memePlaceholder = groundRE(`(?i)^[\x{9}-\x{d}\x{20}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*(?:\[|【)(?:表情包|meme|sticker)(?:[\x{9}-\x{d}\x{20}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*[:：][\x{9}-\x{d}\x{20}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*[^\]】\n]*)?(?:\]|】)[\x{9}-\x{d}\x{20}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*$`)

func hasMemePlaceholder(segments []dispatch.Segment) bool {
	for _, segment := range segments {
		for _, part := range fromRawParts(segment.Parts) {
			if get(part, "type") == "text" && memePlaceholder.MatchString(groundUnits(str(get(part, "text")))) {
				return true
			}
		}
	}
	return false
}

func (r *Runtime) outgoingEligible(c *TurnContext, segments []dispatch.Segment) (bool, error) {
	for _, extension := range r.exts() {
		if extension.ValidateOutgoingScoped != nil {
			if valid, err := extension.ValidateOutgoingScoped(r.services.Authority, c, cloneSegments(segments)); err != nil || !valid {
				return valid, err
			}
		}
	}
	return true, nil
}
