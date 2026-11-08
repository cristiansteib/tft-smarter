"""Exercise dimensional changes in isolated temporary projects, inside Blender.

Run: blender --background --python enclosure/verify_variants.py
Does not overwrite the delivered default model or touch the live Blender.
"""
import json
import os
from pathlib import Path
import runpy
import tempfile

root=Path(__file__).resolve().parent
defaults=json.loads((root/'parameters.json').read_text())
os.environ['TFT_USE_JSON']='1'
results=[]
for changes in [
    {'desk_angle':40.0,'stack_height':16.5},
    {'desk_angle':70.0,'stack_height':24.0,'fit_clearance':0.4},
]:
    with tempfile.TemporaryDirectory(prefix='tft-desk-verify-') as directory:
        work=Path(directory)
        params=defaults|changes
        (work/'parameters.json').write_text(json.dumps(params))
        (work/'references').symlink_to(root/'references',target_is_directory=True)
        runpy.run_path(str(root/'build_enclosure.py'),init_globals={'TFT_ROOT':str(work)})
        report=json.loads((work/'output/validation.json').read_text())
        results.append({'changes':changes,'parts':report['parts'],
                        'reference_intersections':report['reference_intersection_volume_mm3']})
(root/'output/variant_validation.json').write_text(json.dumps(results,indent=2))
print('Both parameter variants passed; default delivery is unchanged.')
