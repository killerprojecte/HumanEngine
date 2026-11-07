// Package nodeicu owns the single process-wide initialization of the pinned
// Node ICU data shared by native collation, Date.parse timezone conversion and participation-hour formatting.
package nodeicu

import "errors"

var ErrUnavailable = errors.New("nodeicu: binary was built without pinned native ICU")

const DataSHA256 = "f294f56707f4bbbc669d291ff537f632eb8d363a8ea60b70b091cdf273ea0499"
