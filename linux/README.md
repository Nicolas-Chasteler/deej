# The actual deployment

Everything in here is live on `desktop` (CachyOS, KDE Plasma 6, Wayland). It's
tracked because it isn't derivable from the code: the channel order comes from
how the box got wired, and most of the command choices are the second thing
tried, not the first.

| File | Lives at |
|---|---|
| `config.yaml` | `/opt/deej/config.yaml` |
| `deej-mic-toggle` | `~/.local/bin/deej-mic-toggle` |
| `systemd/ydotoold.service` | `~/.config/systemd/user/ydotoold.service` |
| `systemd/deej.service.d/10-ydotool.conf` | `~/.config/systemd/user/deej.service.d/` |

The binary is built from this tree and copied to `/opt/deej/deej`. That file is
root-owned but the directory isn't, so replacing it means `rm` then `cp` — a
plain `cp` over the top gets permission denied.

## The hardware

Five sliders on A0–A4, four MX switches on D2–D5, an Arduino Nano (A000005).
Sketch is `arduino/deej-5-sliders-4-buttons`. Switches arrive as extra channels
appended after the sliders, so they're channels 5–8, and a pressed one reports
1023.

**Channels run back-to-front.** Channel 8 is the switch nearest the back wall,
channel 5 the one nearest the user. That's the wiring as built. Channel order
follows the `buttonInputs` array, not physical position, so if a future rebuild
comes out shuffled, reorder that array rather than resoldering.

Buttons are laid out as two pairs — microphone at the back, media at the front.
Four unrelated functions need memorising; two pairs don't.

## Why ydotool, and why only once

KWin advertises `zwp_input_method_v1` but **not**
`zwp_virtual_keyboard_manager_v1`, so `wtype` cannot work here. `xdotool` is
X11-only and can't reach native Wayland windows. `ydotool` works because it
injects through `/dev/uinput`, below the compositor — the event is
indistinguishable from a real keypress, which is what lets Discord catch it
globally.

`/dev/uinput` carries a uaccess ACL for the logged-in user, so `ydotoold` runs
as a **user** service. No root, no group changes. The socket path is set
explicitly in the unit because the default has moved between releases, and deej
is told where it is via the drop-in.

Only the Discord mute button uses a keystroke, because Discord exposes mute no
other way. Everything else calls a real interface — those don't need a key bound
anywhere and don't care which window has focus. **Prefer that whenever the
target offers it.**

F16 specifically because this keyboard stops at F15, so nothing else can send it
and no app already has it bound.

## The mic toggle

EasyEffects can't be retargeted from a script. 8.2.9's CLI does presets and
bypass only, there's no D-Bus method for it, and editing `easyeffectsrc` under a
running instance just gets overwritten on exit.

The lever that does work is `useDefaultInputDevice`. With that on, EasyEffects
follows PipeWire's default source, so the script sets the default to the mic it
wants, waits for EasyEffects to pick it up, then hands the default back to
`easyeffects_source`. That last step matters — leaving the default on raw
hardware means anything following it bypasses the whole chain.

Handing it back does **not** make EasyEffects retarget onto its own virtual
node; it holds the hardware device it last moved to. Verified, not assumed, and
it's the reason the two-step is safe.

The script reads the live PipeWire graph to decide which way to toggle, never
the config file: KConfig defers writes, so `easyeffectsrc` on disk lags the
running state and reading it gets the answer backwards.

## Things learned the hard way

- **`playerctl` needs `-p spotify,%any`.** Bare `playerctl` follows the most
  recently active player, which sounds smarter and isn't — with a YouTube tab
  open the media buttons silently control Firefox. The comma list is a priority
  order and `%any` is the fallback.
- **The Antlion is wireless and vanishes from PipeWire when switched off.** The
  toggle checks its target exists before switching, rather than leaving you on a
  mic that isn't there.
- **Input volumes are per-device and persist.** WirePlumber keeps them in
  `default-routes`, so the Antlion's 82% trim (matching it to the PCM2902)
  survives swapping and reboots. The toggle never touches volumes.
- **Buttons fire on the rising edge only.** There's no release event, so
  push-to-talk isn't possible without changing `serial.go` and the mapping
  format to carry both halves.
- **Config reloads live.** Reassigning a button is a one-line edit with no
  restart. Reflashing is only needed to add a switch or move a pin.
