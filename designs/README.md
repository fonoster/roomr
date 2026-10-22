# Designs

Source for Roomr's visual assets, as in `fonoster/fonoster` and `fonoster/routr`.

| File | What |
| --- | --- |
| `roomr.pen` | The [Pencil](https://pencil.dev) source: the README banner (1280 x 460) and the LinkedIn image (1200 x 627). Export from Pencil; do not edit the PNGs by hand. |
| `assets/banner.png` | The README banner export. `../assets/banner.png` is the copy the README uses; keep them identical. |
| `assets/roomr-linkedin.png` | The LinkedIn image (square corners, 2400 x 1254). |
| `assets/dots.png`, `assets/dots-linkedin.png` | The dot-grid background layers used by the two frames, drawn like Routr's banner background. |

The palette is Fonoster's shared component library, GoodTok brand: `#181818` background, `#FF9965`
accent (with `#C46C3F` and `#27150C`), white and gray type. The logo is "roomr" in Poppins
SemiBold with an accent dot, as in Routr. Diagrams in the docs are Mermaid, so they stay text, diff
well and are readable by AI tools; only the banner and the social image are images.
