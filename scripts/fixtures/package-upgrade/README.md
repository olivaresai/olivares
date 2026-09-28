# Package upgrade fixtures

`ccf7ea20/` holds the four nFPM maintainer scripts exactly as dev main
ccf7ea201e203b0b7af20f95fc97fbc5fd545442 shipped them
(`git show ccf7ea20:packaging/nfpm/<name>`). They are the OLD side of the
old→new rows in `scripts/test-package-upgrade-scripts.sh`. The test checks
their digests before it runs them, so an edited copy is refused, not measured.

The old preremove.sh does not read its argument. On a Debian `prerm upgrade`
or an RPM `%preun 1` it runs `olivares uninstall --preserve`, which stops and
disables a systemd service (uninstall.go stopService). The old→new rows
measure that defect and report it with the recovery the new postinstall
names. They are never counted green.
