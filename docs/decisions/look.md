# The window is paper and ink, with one accent

Status: accepted; `app/theme.go` sets it for every window.

## Context

MyGo's default theme is a neutral form palette: white or near-black, gray
surfaces, a blue accent. It is the right default for settings and tools,
and the wrong one for an app whose work is reading: a mail window drawn
in it reads as an inbox form, with the mail as rows of a table.

The window now lists the ways into the mail rather than a folder tree
([navigation.md](navigation.md)), and that asks for a look that says mail
is something to read: calm, mostly text, one thing calling for attention.

## Decision

The app sets the theme once per frame (`useTheme`), from MyGo's light or
dark theme as the system's appearance decides, and every widget follows
it: the mail window, settings, contacts, rules and compose.

- **Paper and ink.** Warm neutrals instead of pure white and black:
  `#fcfbf9` on `#1c1b19` in light, `#15161a` on `#eeede9` in dark.
- **One accent.** A deep teal (`#0f6e63`, `#16a394` in dark) for what is
  chosen, what asks for an answer, and what the keyboard focuses. Nothing
  else is colored.
- **A chosen row is tinted, not filled.** The accent at 10% keeps the
  mail readable while it is chosen; a solid fill turns the list into a
  block of color, and a mail list is mostly read rather than acted on. A
  hovered row is the surface at 55%.
- **Room instead of lines.** Rows keep one hairline at 60% of the border
  color, spaced by padding; the stream's band is a line of type
  (`28 need you · 1 with a code · 72 just news`) rather than a row of
  boxes.
- **Outlined, not filled, where a row shows its own extras.** A code,
  amount or date is a pill outlined in the border color, on the row's
  background: it belongs to the message's line, not on top of it.
- Radius 10, spacing 4 as MyGo counts it, and the system's font.

## Consequences

- A color means one thing: teal is the accent, so a teal tint always
  says selected, focused or needs you. Red, amber and green stay for what
  is wrong, late or done, and nothing else takes them.
- Increased-contrast and reduced-motion preferences are not applied to
  the palette: `useTheme` replaces the theme MyGo derived from them.
  Motion preferences still hold, as the app's animations are MyGo's.
- The sidebar is as wide as before (222): one more unit of width there
  took the room the reading pane's toolbar needs at the window's minimum
  size.
- Light and dark are One Palette's two appearances, not two designs; a
  check must look at both (`TestThemeIsTheApps` samples the reading
  surface in each).

## Open

- A type scale of its own (a larger base size, a display face for a
  conversation's subject) would make the reading pane read as a page. It
  needs room at 1000×640 first.
- Whether the list keeps its hairline at all once rows are spaced
  further.
- Whether the accent should follow the system's accent color, as native
  apps do, instead of one fixed teal.