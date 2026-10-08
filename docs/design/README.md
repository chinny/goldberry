# Design references

Snapshots of the two design sources Goldberry is built from. The live originals are the source of truth; refresh these copies when they change.

| What | Live source | In this repo |
| --- | --- | --- |
| Design plan (architecture, money model, phasing) | [Goldberry — Design Plan](https://claude.ai/artifact/M1fZn8vas1tyyRnjCRNZjx) | [`plan.md`](plan.md) |
| Design board (foundations + screens) | [Goldberry Design](https://claude.ai/artifact/YAWb4TxkmEHqaimx29Uqxb) | [`board/`](board/) sources, [`screens/`](screens/) renders |

## Screens

| Board | Render | Notes |
| --- | --- | --- |
| Foundations & components | [Main.png](screens/Main.png) | Tokens, type scale, components |
| Kid · PIN sign-in | [KidLogin.png](screens/KidLogin.png) | Big-digit pad, 4–6 dots |
| Kid · Home | [KidHome.png](screens/KidHome.png) | One big number per jar, holds in words |
| Kid · Ask for money | [KidRequest.png](screens/KidRequest.png) | Withdrawal request form |
| Kid · Break my lock | [LockGauntlet.png](screens/LockGauntlet.png) | Self-lock override gauntlet (Phase 4+) |
| Parent · Dashboard | [AdminHome.png](screens/AdminHome.png) | Pending queue + kid cards |
| Parent · Notifications | [Notifications.png](screens/Notifications.png) | Bell with inline approve/deny |
| Parent · Add or remove funds | [AddFunds.png](screens/AddFunds.png) | Amount, split, chips, private note |
| Parent · One kid | [AdminKid.png](screens/AdminKid.png) | Jars, locks, ledger, account actions |
| Desktop · Parent dashboard | [DesktopAdmin.png](screens/DesktopAdmin.png) | |
| Desktop · One kid & ledger | [DesktopAdminKid.png](screens/DesktopAdminKid.png) | |
| Desktop · Kid home | [DesktopKidHome.png](screens/DesktopKidHome.png) | |

The `board/*.dc.html` files are the canvas's Design Component sources. They need the canvas runtime to render exactly; `tools/` holds a minimal stand-in (`support.js`) and a Playwright script used to make the PNGs:

```sh
mkdir -p /tmp/board && cp docs/design/board/* docs/design/tools/support.js /tmp/board/
node docs/design/tools/render.mjs /tmp/board docs/design/screens   # needs the playwright package
```

## Tokens ("River & gold")

The app's stylesheet (`internal/web/static/app.css`) defines these as CSS custom properties.

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `--gb-ground` | `#F4F6F5` | `#0E1A19` | Page background |
| `--gb-surface` | `#FFFFFF` | `#152523` | Cards, inputs |
| `--gb-sunk` | `#E9EEEC` | `#0A1312` | Segmented controls, wells |
| `--gb-line` | `#D3DCD9` | `#2A3D3A` | 1px borders (elevation is a line, not shadow) |
| `--gb-ink` | `#13312E` | `#E7EFEC` | Text |
| `--gb-ink-muted` | `#4F6460` | `#9DB2AD` | Secondary text |
| `--gb-river` | `#1F6F64` | `#5CC2B1` | Primary action (parent confirm), Save jar |
| `--gb-river-strong` | `#155248` | | Hover, credit amounts |
| `--gb-river-tint` | `#DDEEEA` | | Save jar tint |
| `--gb-gold` | `#E2A81B` | `#F0C04A` | Kid's main action, Spend jar |
| `--gb-gold-ink` | `#7A5200` | | Text on gold tints |
| `--gb-gold-tint` | `#FBEFCF` | | Spend jar tint |
| `--gb-berry` | `#B4436C` | `#E68AAB` | Give jar (text `#9A3359`) |
| `--gb-berry-tint` | `#F6E1E8` | | Give jar tint |
| `--gb-danger` | `#B42318` | `#F2877B` | Destructive or lock-breaking acts only |
| `--gb-danger-tint` | `#FBE4E1` | | |

- **Type:** Bricolage Grotesque (display, money; 800/600) over Atkinson Hyperlegible Next (body; 400/700). Money uses tabular numerals. Scale: money-xl 56/60, money-l 36/40, display 32/38, title 22/28, body 17/26, label 15/20 bold, caption 14/20 muted.
- **Space:** `--gb-s1`…`--gb-s8` = 4, 8, 12, 16, 24, 32, 48, 64 px.
- **Radius:** sm 8 (inputs, chips), md 12 (buttons), lg 20 (cards, jars), pill.
- **Touch targets:** ≥ 48 px (44 minimum); PIN keys 72 px.
- **Jar identity:** Spend = gold, Save = river, Give = berry, custom = slate. Hue is identity, never status: status always comes with a word.
- **Money:** credits in river with a `+`, debits in ink with a true minus `−`. Never colour alone.
