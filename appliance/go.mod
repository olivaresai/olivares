module github.com/olivaresai/olivares/appliance

go 1.26.6

require (
	github.com/godbus/dbus/v5 v5.2.2

	// The setup-token owner. core/secure is standard-library-only, so this edge adds no
	// third-party tree to the appliance; the appliance mints the product's own token
	// through the owner instead of duplicating its format.
	github.com/olivaresai/olivares/core v0.0.0
)

require golang.org/x/sys v0.48.0 // indirect

replace github.com/olivaresai/olivares/core => ../core
