# TFT DESK / 01

Parametric Blender enclosure for the Mega 2560 and the photographed 3.5-inch
shield. **Fit-check prototype: the actual shield registration and assembled
height still need measurement.** No physical print or fit test has been done.

See the [bill of materials](../BOM.md) for quantities, hardware specifications
and Amazon Spain search links.

## Files

- `output/tft_desk_parametric.blend`: assembled model, print layout, exploded
  view, editable parameters, and embedded generator.
- `build_enclosure.py`: reproducible Blender Python source.
- `parameters.json`: millimetre defaults; `output/parameters_used.json` records
  the actual values used for the most recent build.
- `output/01_rear_tray.stl`, `02_front_bezel.stl`, `03_desk_cradle.stl`:
  the three enclosure parts, individually oriented for printing.
- `output/04_board_fit_gauge.stl`, `05_window_fit_gauge.stl`: optional thin
  fit templates. Print these first.
- `output/validation.json`: mesh topology, dimensions, and reference-solid
  interference checks. Hardware and studio objects are excluded from STLs.

Default case: **120 × 75 × 29.8 mm**. Default cradle: **112 × 100 mm** footprint,
with the screen **55° above the desktop**. The stand lifts off for servicing;
its seat and front stop retain the case by gravity. It is not a locking mount.
Use the rubber feet and check stability with your actual cable attached.

**USB is on the right when looking at the display**, with DC above it. The
`usb_on_right = 1` parameter rotates the case and physical board together by
180° in the screen plane. The illustrative dashboard remains upright. Set the
actual firmware's screen rotation to match this physical orientation if needed.
STLs use neutral print coordinates: orient the completed case with the ports
on the right when placing it in the stand.

## What is measured, sourced, or assumed?

| Item | Default | Evidence |
| --- | --- | --- |
| Mega board envelope | 101.6 × 53.34 mm | Arduino Rev3e Eagle outline |
| Four outer mounting holes | See table below | Arduino Rev3e Eagle holes |
| USB-B centre across PCB | y = 38.1 mm | Arduino Eagle footprint |
| DC connector body centre | y = 7.7978 mm | Arduino footprint outline transformed to board coordinates |
| Shield envelope | 86 × 57 mm | Conservative starting value for the photographed shield; not a confirmed part identification |
| Example compatible shield | 85.49 × 55.63 mm | LCDWIKI MAR3501/MAR3502 documentation |
| Typical active display area | 73.44 × 48.96 mm | LCDWIKI specification |
| Viewing aperture | 76.4 × 51.9 mm | Design allowance around that active area |
| Shield centre in case | x = 0, y = 0.3 mm | Provisional registration from the photographs |
| Mega underside to glass | 18.5 mm | **Unmeasured design assumption** |
| PCB thickness / socket height | 1.6 / 11 mm | Nominal reference envelopes; vertical positions need checking |
| Bezel-to-glass air gap | 0.7 mm | Design clearance; case must not compress the touch panel |
| Lid locating-lip clearance | 0.3 mm per side | FDM starting allowance, tune for your printer |

Arduino hole centres in the **original Eagle coordinate system**:

| x (mm) | y (mm) |
| --- | --- |
| 13.97 | 2.54 |
| 15.24 | 50.8 |
| 90.17 | 50.8 |
| 96.52 | 2.54 |

The two inner Arduino holes are intentionally unused. Case coordinates put the
board near its centre: add `board_origin_x = -50.7744` and
`board_origin_y = -26.6572` to the Eagle coordinates. X points from the USB end
toward the far digital header; Y points from the power header to the digital
header; Z points from the board toward the glass.
These are the native design coordinates, before the right-side-USB assembly
rotation. The final assembled view reverses X and Y in the screen plane.

Sources retrieved 2026-11-07:

- [Arduino Mega 2560 documentation](https://docs.arduino.cc/hardware/mega-2560/)
- [Original Arduino Rev3e Eagle CAD archive](https://content.arduino.cc/assets/MEGA2560_Rev3e.zip)
  — retained unchanged in `references/`; used to construct the PCB reference.
- [LCDWIKI 3.5-inch UNO shield](https://www.lcdwiki.com/3.5inch_Arduino_Display-UNO)
- [LCDWIKI dimensional drawing](https://www.lcdwiki.com/images/8/84/MAR3502-size.PNG)
  — retained in `references/` for comparison, not proof of the photographed variant.
- [QD3502 LCD specification](https://www.lcdwiki.com/res/MAR3501/QD-TFT3502%20specification_v1.1.pdf)

Reference assets remain their respective authors' material. The hardware
geometry is a dimensional reference, not a detailed or certified vendor STEP
model. The display graphics are illustrative.

## Confirm fit before the full print

1. Print the two thin gauges at **100% scale, millimetres**. Check the board
   holes and the visible screen aperture. Do not press a printed edge against
   the glass. The aperture gauge does not verify shield-to-Mega registration.
2. Measure `stack_height`: underside of Mega PCB to front surface of glass,
   with the shield fully seated. Exclude solder pins below the PCB.
3. Measure shield outer width/height and its centre relative to the Mega.
   Update `shield_width`, `shield_height`, `shield_center_x/y`. Measure the
   active area's offset if asymmetric and update `window_offset_x/y`.
4. Check socket Z heights and your cable boots. Default USB opening is
   18 × 16 mm; DC opening is 14 × 14 mm. Their centres track the PCB height.
   The holes allow recessed sockets to accept plugs; large boots may need
   larger openings. There is no separate SD-card or reset-button opening:
   access these by removing the lid and, where needed, unplugging the shield.
5. Regenerate and inspect `validation.json`. Fit the tray to the PCB first,
   then confirm that the lid seats on the tray without touching the glass.

## Edit in Blender

1. Open the `.blend`, choose scene `TFT_DESK_ASSEMBLY`.
2. Select `TFT_DESK_PARAMS` in collection `TFT_DESK_00_PARAMETERS`.
3. Open **Object Properties → Custom Properties**. Change dimensions or
   `desk_angle` (degrees above the desk; supported range 40–70°).
4. In the Text Editor, select `build_enclosure.py` and **Run Script / Alt-P**.
   This rebuilds generated geometry, rechecks it, exports all STLs, and saves
   the blend. It preserves unrelated scenes and objects. These are scripted
   parameters: changing a property alone does not update the mesh.

The live parameter object overrides scalar JSON values. For JSON-only builds,
start a fresh Blender session, or run from a terminal:

```sh
TFT_USE_JSON=1 blender --background --python enclosure/build_enclosure.py
```

Keep the whole `enclosure/` directory together when moving the project: the
generator uses the JSON and CAD reference alongside it. Edit `board_holes` in
JSON for a board variant. Larger dimensional changes can invalidate geometry;
failed validation aborts the build and must be resolved before printing.

The generator locates its inputs relative to the source or saved Blender file;
it does not require a particular user's workspace path. Shared builds use
relative render/file-browser paths and disable render stamps. For publication,
generate from a fresh Blender session (`--factory-startup`) so unrelated scenes
and user data are not included in the saved project.

`TFT_DESK_PRINT_LAYOUT` is an inspection layout, not a single build plate to
export as one mesh. Slice the individual STLs. `TFT_DESK_EXPLODED` shows the
assembly order; the hardware proxies in that scene are not printable parts.

## Printing and hardware

Suggested starting settings: 0.4 mm nozzle, 0.2 mm layers, four perimeters,
five top/bottom layers, 20–30% infill. PLA is suitable for an ordinary indoor
desk; PETG gives more temperature margin. Calibrate small holes for your
printer. These settings and overhangs have not been physically tested.

| Part | Supplied STL orientation | Support guidance |
| --- | --- | --- |
| Rear tray | Exterior rear face on bed; cavity up | USB/DC roofs bridge 18/14 mm. Use local support if your printer cannot bridge these cleanly. Vent spans are 3 mm. |
| Front bezel | Cosmetic front face down; locating lip up | Aperture chamfer is 45°. No broad internal supports intended; use a clean smooth bed for the visible face. |
| Cradle | Flat base down | Front plinth supports the seat; gussets rise from base. Rubber-foot recesses bridge about 10 mm; inspect slicer preview. |
| Fit gauges | Flat | No supports expected. |

Hardware:

- **4 × M2.5 × 6 mm screws** for the Mega into 2.2 mm printed pilot holes.
  Prefer nylon heads near electronics. Seat gently; the model has 4.6 mm blind
  pilot depth and 5 mm pedestals. Check that the board sits flat without
  solder joints touching the floor.
- **4 × M3 × 10 mm button-head screws**, head diameter at most 6.5 mm, for
  the lid into 2.6 mm printed pilots. Pilots are 9.6 mm deep and the
  counterbore leaves 1.2 mm of lid material under the head. Cut threads slowly
  or tap; do not force an undersized printed hole. No heat-set inserts are
  specified by this version.
- **4 × adhesive rubber feet**, nominal Ø10 × 2 mm. The cradle has
  Ø10.4 × 0.8 mm shallow recesses.

Assembly: remove the shield, screw the Mega onto its four pedestals, re-seat
the shield, fit the front bezel, then tighten the four lid screws lightly.
Add feet and lower the case into the cradle. It lifts out without tools. The
four lid screws remain accessible after lifting it out.

## Validation limits

The generator rejects open/non-manifold edges, loose vertices, degenerate
faces, multiple disconnected mesh components, and nonpositive volume. It
also computes solid intersections between case parts and the modeled PCB,
USB, DC and shield envelopes, and checks the assembled cradle against the
case. A tolerance below 0.01 mm³ accommodates numerical contact noise.

The default build and two dimensional variants were exercised in Blender:
40° with a 16.5 mm stack, and 70° with a 24 mm stack and 0.4 mm lip clearance.
All passed the same mesh and reference-interference checks; see
`output/variant_validation.json`. Re-run these checks with
`blender --background --python enclosure/verify_variants.py` after modifying
the generator. Export files are staged until all build checks pass.
`python3 enclosure/verify_stls.py` independently reads the binary STL files,
checks their triangle edges and signed volumes, and confirms they sit at Z=0.
Results are recorded in `output/stl_validation.json`.

These checks establish properties of the digital model, not real-world fit,
wall strength, slicer behavior, full electronic-component clearance, or a
tip-over rating. The glass frame and small electronic details are simplified;
printer error, shield clone variations, cable force, and touch loads require
the physical fit checks above.
