// Package kshan holds Kronos's jobs for Kshan, the phone's widget app (module "kshan").
// Kshan replaced RahiOS; its vault folder is logs/kshan/ (daily logs, inbox, events, weather).
//
//	weather.go   kshan_weather – location.md → open-meteo → logs/kshan/weather/weather.md (Kshan Clock widget)
//	events.go    kshan_events  – Kshan's `e` drafts (logs/kshan/events.md) → data/event.md
//	when.go      reads a date and a time out of a draft's text ("dentist fri 5pm")
//
// Kronos never writes the phone's own files (daily logs, events.md, config.md, weather/location.md).
// Pulse reads the daily logs (internal/pulse); messages for the phone go to paths.phone_inbox.
package kshan
