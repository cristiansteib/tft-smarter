"""Independent STL check using only standard Python; no Blender required."""
from collections import Counter
import json
from pathlib import Path
import struct

output=Path(__file__).resolve().parent/'output'
results={}
files=sorted(output.glob('*.stl'))
assert len(files)==5, 'Expected three enclosure parts and two fit gauges.'
for path in files:
    data=path.read_bytes()
    count=struct.unpack_from('<I',data,80)[0]
    assert len(data)==84+count*50, 'Invalid STL byte length'
    edges=Counter(); vertices=[]; volume=0; degenerate=0
    for i in range(count):
        record=struct.unpack_from('<12fH',data,84+i*50)
        v=[tuple(round(x,5) for x in record[j:j+3]) for j in (3,6,9)]
        vertices+=v
        a,b,c=v
        ab=[b[k]-a[k] for k in range(3)]; ac=[c[k]-a[k] for k in range(3)]
        cross=[ab[1]*ac[2]-ab[2]*ac[1],ab[2]*ac[0]-ab[0]*ac[2],ab[0]*ac[1]-ab[1]*ac[0]]
        if sum(x*x for x in cross)<1e-14:
            degenerate+=1
        volume+=(a[0]*(b[1]*c[2]-b[2]*c[1])+a[1]*(b[2]*c[0]-b[0]*c[2])+a[2]*(b[0]*c[1]-b[1]*c[0]))/6
        for j in range(3):
            edges[tuple(sorted((v[j],v[(j+1)%3])))]+=1
    bad=sum(n!=2 for n in edges.values())
    bounds=[round(max(v[k] for v in vertices)-min(v[k] for v in vertices),3) for k in range(3)]
    results[path.name]={
        'triangles':count,'nonmanifold_edges_after_welding_0.00001mm':bad,
        'degenerate_triangles':degenerate,'signed_volume_mm3':round(volume,2),
        'bounds_mm':bounds,'minimum_z_mm':min(v[2] for v in vertices),
    }
    assert bad==0 and degenerate==0 and volume>0, (path.name,results[path.name])
    assert abs(results[path.name]['minimum_z_mm'])<.00001
(output/'stl_validation.json').write_text(json.dumps(results,indent=2))
print('PASS: five closed STL meshes, positive volumes, no degenerate triangles; all rest at Z=0.')
