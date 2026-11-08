# Bill of materials — TFT desktop enclosure

For one Arduino Mega 2560 + 3.5-inch ILI9486 shield enclosure, with USB on
the right. Based on the current [enclosure design](enclosure/README.md) and
[parameters](enclosure/parameters.json). Updated: **2026-11-08**.

**Shopping shortlist:** reuse your Mega, display and USB cable; obtain four
M2.5 × 6 mm screws, four M3 × 10 mm button-head screws, four adhesive rubber
feet, and filament if needed.

Amazon links below are **Amazon Spain searches**, not verified product
listings or confirmed stock. Select the exact size and pack contents on the
product page. Prices and delivery have not been verified.

## Purchased hardware

Quantities are the number used in one assembly, not the number of retail packs.

| ID | Qty | Item / specification | Purpose and fit requirement | Possible Amazon link |
| --- | ---: | --- | --- | --- |
| H1 | 4 | **M2.5 × 6 mm machine screws**, nylon preferred; small pan or cylindrical heads, not countersunk | Secure the Mega to the printed pedestals. Length is measured under the head. Printed pilots are Ø2.2 mm and 4.6 mm deep. Check head clearance beside the board headers. | [M2.5 × 6 nylon screws](https://www.amazon.es/s?k=tornillos+nylon+M2.5+x+6+mm) |
| H2 | 4 | **M3 × 10 mm button-head machine screws**, hex drive, ISO 7380-1 style | Secure the front bezel. Choose head diameter **≤6.5 mm**; head height **≤2 mm** if it must sit within the recess. Length is measured under the head. Printed pilots are Ø2.6 mm and 9.6 mm deep. Avoid countersunk or tall socket-cap heads. | [M3 × 10 button-head screws](https://www.amazon.es/s?k=tornillos+M3+x+10+ISO+7380-1+cabeza+boton) |
| H3 | 4 | **Adhesive rubber or silicone feet, Ø10 × 2 mm**, flat circular pads | Fit the cradle's Ø10.4 × 0.8 mm recesses. Check that 10 mm is the diameter and 2 mm the total pad thickness. Larger furniture feet will not fit the current pockets. | [Ø10 × 2 mm adhesive feet](https://www.amazon.es/s?k=patas+goma+silicona+adhesivas+10mm+2mm) |
| H4 | As needed | **PLA or PETG filament**, diameter supported by your printer | Print the parts below. One colour is sufficient; the graphite body, light bezel and teal stand in the preview are optional colour choices. Use your slicer's estimate for material quantity. | [PLA filament](https://www.amazon.es/s?k=filamento+PLA+impresora+3D) · [PETG filament](https://www.amazon.es/s?k=filamento+PETG+impresora+3D) |

Buying a small pack of each screw size provides spares. An assortment is only
useful if it explicitly contains **M2.5 × 6** and **M3 × 10 button-head** screws;
many kits contain only M2/M3 or the wrong head style.

The current design uses screws directly in printed pilots. **No nuts,
separate standoffs, washers, heat-set inserts, hinges or stand fasteners are
required.** Heat-set inserts would require changes to the printed bosses.
Nylon screws should not be forced to cut tight threads: prepare the pilot
threads first if needed and tighten gently.

## Electronics and cable — reuse the existing parts

The supplied photos show the board, shield and USB cable already available.
The links here are for replacements or a second build.

| ID | Qty | Item | Compatibility to check | Possible Amazon link |
| --- | ---: | --- | --- | --- |
| E1 | 1 | Arduino Mega 2560 Rev3, full-size board | Match the mounting-hole pattern and USB-B/barrel-jack layout. A Mega Pro/Mini or USB-C clone is not an assumed mechanical replacement. | [Mega 2560 Rev3 USB-B](https://www.amazon.es/s?k=Arduino+Mega+2560+Rev3+USB+B) |
| E2 | 1 | 3.5-inch, 480 × 320 ILI9486 TFT shield compatible with the existing setup | Match the shield pinout, PCB dimensions, screen position and assembled height. A 3.5-inch diagonal or ILI9486 label alone does not establish compatibility; do not substitute a Raspberry Pi or SPI module without redesign. | [3.5-inch ILI9486 Arduino shield](https://www.amazon.es/s?k=pantalla+3.5+ILI9486+480x320+shield+Arduino+Mega) |
| E3 | 1 | USB **data** cable, USB-B male at the Mega; USB-A or USB-C at the PC | Reuse the pictured cable if its moulded boot fits the right-side opening, nominally 18 × 16 mm. A straight connector avoids uncertainty about elbow direction. | [USB-A to USB-B data cable](https://www.amazon.es/s?k=cable+USB+A+a+USB+B+datos+1m) · [USB-C to USB-B data cable](https://www.amazon.es/s?k=cable+USB+C+a+USB+B+datos+1m) |

The PC data connection uses USB. A separate barrel-jack power supply is not
included in this BOM; the enclosure simply preserves access to that connector.

## Printed parts

Print at **100% scale in millimetres**. The STLs already have their intended
bed orientation. Print the two gauges first; they are not installed in the
finished assembly.

| ID | Qty | Part | File | Nominal print bounds (mm) |
| --- | ---: | --- | --- | --- |
| P1 | 1 | Rear tray with integrated PCB pedestals and lid bosses | [01_rear_tray.stl](enclosure/output/01_rear_tray.stl) | 120 × 75 × 26.6 |
| P2 | 1 | Front bezel with locating lip | [02_front_bezel.stl](enclosure/output/02_front_bezel.stl) | 120 × 75 × 6 |
| P3 | 1 | Desktop cradle, default 55° viewing angle | [03_desk_cradle.stl](enclosure/output/03_desk_cradle.stl) | 112 × 100 × 66.883 |
| G1 | 1, fit check | Mega mounting-hole gauge | [04_board_fit_gauge.stl](enclosure/output/04_board_fit_gauge.stl) | 108 × 60 × 1.6 |
| G2 | 1, fit check | Display-window gauge | [05_window_fit_gauge.stl](enclosure/output/05_window_fit_gauge.stl) | 90 × 61 × 1.6 |

For layer height, walls, support guidance and assembly order, use the
[printing guide](enclosure/README.md#printing-and-hardware). The three case
parts can be printed separately; they do not need to share one build plate.

## Tools — only if not already available

| Qty | Tool | Notes / possible Amazon link |
| ---: | --- | --- |
| 1 | Driver matching the M2.5 screw head | [Precision screwdriver set](https://www.amazon.es/s?k=destornilladores+precision) |
| 1 | Hex key matching the M3 button-head screws | Commonly 2 mm for this style; verify the chosen screw listing. [Metric hex-key set](https://www.amazon.es/s?k=juego+llaves+allen+metricas+2mm) |
| 1 | Working caliper | Needed to confirm the stack height and shield registration. [Digital caliper](https://www.amazon.es/s?k=calibre+digital+150mm) |
| Optional | M2.5 and M3 taps suitable for blind holes, with hand holder | For preparing printed pilot threads if necessary. Respect the shallow blind-hole depths; do not drill or tap through the case floor. [Small metric tap set](https://www.amazon.es/s?k=machos+roscar+M2.5+M3+portamachos) |

## Before the full print

The current model assumes **18.5 mm from the underside of the Mega PCB to
the front of the display glass**. This and the shield's position remain
unverified. Confirm them, check the two gauges and cable fit, and regenerate
the model if necessary. Digital mesh checks do not establish physical fit.
