"""TFT DESK / 01. Run in Blender's Text Editor or blender --background --python.

All geometry coordinates and STL coordinates are millimetres. The live PARAMS
object overrides scalar JSON defaults on rebuild. Delete PARAMS or set
TFT_USE_JSON=1 in the environment to reset from parameters.json.
Only generated TFT_DESK_* scenes/collections/objects are replaced.
"""
import bpy
import bmesh
import math
import os
import json
import struct
import tempfile
import zipfile
import xml.etree.ElementTree as ET
from pathlib import Path
from mathutils import Vector, Matrix

_source_path = Path(globals().get('__file__', 'build_enclosure.py')).resolve()
_candidates = [_source_path.parent, Path.cwd()/'enclosure', Path.cwd()]
if bpy.data.filepath:
    _candidates.insert(0, Path(bpy.data.filepath).resolve().parent.parent)
_default_root = next((p for p in _candidates if (p/'parameters.json').is_file()), None)
_root_override = globals().get('TFT_ROOT')
if _root_override is None and _default_root is None:
    raise FileNotFoundError('Keep the Blender file in enclosure/output, or run the generator from the project directory.')
ROOT = Path(_root_override if _root_override is not None else _default_root)
PREFIX = 'TFT_DESK_'
FINAL_OUT = ROOT / 'output'
FINAL_OUT.mkdir(exist_ok=True)
# Invalid parameter combinations must not replace the last valid STL set.
_build_dir = tempfile.TemporaryDirectory(prefix='.build-',dir=ROOT)
OUT = Path(_build_dir.name)
P = json.loads((ROOT / 'parameters.json').read_text())
old_control = bpy.data.objects.get(PREFIX + 'PARAMS')
if old_control and os.environ.get('TFT_USE_JSON') != '1':
    for key, value in P.items():
        if isinstance(value, (float, int)) and key in old_control:
            P[key] = float(old_control[key])

W, H, T, F = [P[k] for k in ('case_width', 'case_height', 'wall', 'floor')]
PCB_Z = F + P['standoff_height']
GLASS_Z = PCB_Z + P['stack_height']
SEAM = GLASS_Z + P['glass_clearance']
DEPTH = SEAM + P['lid_thickness']
SX, SY = P['shield_center_x'], P['shield_center_y']
WX, WY = SX + P['window_offset_x'], SY + P['window_offset_y']
SCREWS = [(x, y) for x in (-W/2+6, W/2-6) for y in (-H/2+6, H/2-6)]
HOLES = [(x+P['board_origin_x'], y+P['board_origin_y']) for x,y in P['board_holes']]
assert 40 <= P['desk_angle'] <= 70, 'Supported stand angle: 40 to 70 degrees.'
assert P['glass_clearance'] >= .4 and P['fit_clearance'] >= .15
assert W >= 116 and H >= 74, 'Keep the PCB and corner screw bosses separated.'
assert P['stack_height'] >= 15, 'Stack is too short for the connector reference envelopes.'
assert P['window_width'] < P['shield_width']-2
assert P['window_height'] < P['shield_height']-2
assert abs(SX)+P['shield_width']/2 < W/2-T-P['lip_wall']-P['fit_clearance']
assert abs(SY)+P['shield_height']/2 < H/2-T-P['lip_wall']-P['fit_clearance']

# A new scene preserves unrelated work, including the starting scene.
scene = bpy.data.scenes.new(PREFIX+'ASSEMBLY_NEW')
if bpy.context.window:
    bpy.context.window.scene = scene
for s in list(bpy.data.scenes):
    if s != scene and s.name.startswith(PREFIX):
        bpy.data.scenes.remove(s)
for obj in list(bpy.data.objects):
    if obj.name.startswith(PREFIX):
        bpy.data.objects.remove(obj, do_unlink=True)
for coll in list(bpy.data.collections):
    if coll.name.startswith(PREFIX):
        bpy.data.collections.remove(coll)
scene.name = PREFIX+'ASSEMBLY'
scene.unit_settings.system = 'METRIC'
scene.unit_settings.scale_length = .001

def collection(name):
    c = bpy.data.collections.new(PREFIX+name)
    scene.collection.children.link(c)
    return c

parts_coll = collection('01_PRINTED_PARTS')
refs_coll = collection('02_HARDWARE_REFERENCES_NOT_FOR_PRINT')
studio_coll = collection('03_STUDIO')
control_coll = collection('00_PARAMETERS')
current_coll = parts_coll

def relink(obj, name):
    obj.name = PREFIX+name
    for c in list(obj.users_collection):
        c.objects.unlink(obj)
    current_coll.objects.link(obj)
    return obj

def activate(obj):
    bpy.ops.object.select_all(action='DESELECT')
    obj.select_set(True)
    bpy.context.view_layer.objects.active = obj

def normals(obj):
    bm = bmesh.new()
    bm.from_mesh(obj.data)
    bmesh.ops.recalc_face_normals(bm, faces=list(bm.faces))
    bm.to_mesh(obj.data)
    bm.free()

def mesh(name, verts, faces):
    m = bpy.data.meshes.new(PREFIX+name)
    m.from_pydata(verts, [], faces)
    m.update()
    obj = bpy.data.objects.new(PREFIX+name, m)
    current_coll.objects.link(obj)
    normals(obj)
    return obj

def prism(name, profile, z0, z1):
    n = len(profile)
    v = [(x,y,z) for z in (z0,z1) for x,y in profile]
    f = [tuple(reversed(range(n))), tuple(range(n,2*n))]
    f += [(i,(i+1)%n,(i+1)%n+n,i+n) for i in range(n)]
    return mesh(name,v,f)

def rounded_profile(width,height,r,cx=0,cy=0,steps=12):
    r = min(r,width/2-.001,height/2-.001)
    points=[]
    for x,y,a in [(width/2-r,height/2-r,0),(-width/2+r,height/2-r,90),
                  (-width/2+r,-height/2+r,180),(width/2-r,-height/2+r,270)]:
        for j in range(steps+1):
            t=math.radians(a+90*j/steps)
            points.append((cx+x+r*math.cos(t),cy+y+r*math.sin(t)))
    return points

def rounded(name,w,h,z0,z1,r,cx=0,cy=0):
    return prism(name,rounded_profile(w,h,r,cx,cy),z0,z1)

def box(name, dims, loc):
    bpy.ops.mesh.primitive_cube_add(size=1, location=loc)
    obj=relink(bpy.context.object,name)
    obj.dimensions=dims
    activate(obj)
    bpy.ops.object.transform_apply(location=False,rotation=False,scale=True)
    return obj

def cylinder(name,r,z0,z1,x=0,y=0,n=64):
    return prism(name,[(x+r*math.cos(i*math.tau/n),y+r*math.sin(i*math.tau/n)) for i in range(n)],z0,z1)

def boolean(obj, tool, operation='DIFFERENCE'):
    activate(obj)
    mod=obj.modifiers.new('Precision solid operation','BOOLEAN')
    mod.operation=operation
    mod.solver='EXACT'
    mod.object=tool
    bpy.ops.object.modifier_apply(modifier=mod.name)
    bpy.data.objects.remove(tool,do_unlink=True)
    return obj

def union(obj,tool):
    return boolean(obj,tool,'UNION')

def bevel(obj, amount=.35):
    # Visual micro-chamfer, applied to print geometry as well.
    activate(obj)
    mod=obj.modifiers.new('Print-safe edge break','BEVEL')
    mod.width=amount
    mod.segments=2
    bpy.ops.object.modifier_apply(modifier=mod.name)
    return obj

def material(name,color,metal=0,rough=.4):
    mat=bpy.data.materials.get(PREFIX+name) or bpy.data.materials.new(PREFIX+name)
    mat.diffuse_color=(*color,1)
    mat.use_nodes=True
    node=next(n for n in mat.node_tree.nodes if n.type=='BSDF_PRINCIPLED')
    node.inputs['Base Color'].default_value=(*color,1)
    node.inputs['Roughness'].default_value=rough
    node.inputs['Metallic'].default_value=metal
    return mat

shell_mat=material('Graphite polymer',(.045,.065,.083),rough=.34)
lid_mat=material('Warm silver polymer',(.63,.69,.7),rough=.32)
stand_mat=material('Deep teal polymer',(.025,.25,.28),rough=.32)
pcb_mat=material('Arduino PCB',(.015,.19,.23),rough=.48)
black_mat=material('Socket black',(.012,.016,.023),rough=.5)
silver_mat=material('Connector steel',(.45,.49,.52),metal=.75,rough=.25)
glass_mat=material('Display glass',(.008,.022,.04),metal=.1,rough=.18)
cyan_mat=material('Display cyan',(.02,.83,.85),rough=.5)
muted_mat=material('Display muted',(.1,.29,.34),rough=.5)
white_mat=material('Display text',(.6,.86,.88),rough=.5)
floor_mat=material('Studio background',(.065,.085,.105),rough=.7)

def paint(obj,mat):
    obj.data.materials.clear()
    obj.data.materials.append(mat)
    return obj

# Rear tray: flat floor, four PCB pedestals and four independent lid bosses.
tray=rounded('01_REAR_TRAY',W,H,0,SEAM,P['corner_radius'])
boolean(tray,rounded('tool',W-2*T,H-2*T,F,SEAM+1,max(.8,P['corner_radius']-T)))
for x,y in SCREWS:
    union(tray,cylinder('boss',4.4,F-.2,SEAM,x,y))
    boolean(tray,cylinder('pilot',P['case_pilot_diameter']/2,SEAM-9.6,SEAM+1,x,y))
for x,y in HOLES:
    union(tray,cylinder('standoff',P['standoff_diameter']/2,F-.2,PCB_Z,x,y))
    boolean(tray,cylinder('pilot',P['board_pilot_diameter']/2,PCB_Z-4.6,PCB_Z+1,x,y))
for typ in ('usb','dc'):
    cy=P[typ+'_center_y']+P['board_origin_y']
    cz=PCB_Z+P[typ+'_center_above_pcb']
    # Dimensions are cable-plug clearance envelopes, larger than socket metal.
    boolean(tray,box(typ+'_opening',(T+4,P[typ+'_opening_width'],P[typ+'_opening_height']),(-W/2,cy,cz)))
# Ventilation near the upper and lower long edges, clear of PCB bosses.
for y in (-H/2,H/2):
    for x in (-24,-16,-8,0,8,16,24):
        boolean(tray,box('vent',(3,T+3,7),(x,y,15)))
paint(tray,shell_mat)

# Faceplate with a chamfered viewing opening and a clearance fit locating lip.
lid=rounded('02_FRONT_BEZEL',W,H,SEAM,DEPTH,P['corner_radius'])
lip_outer=(W-2*T-2*P['fit_clearance'],H-2*T-2*P['fit_clearance'])
lip=rounded('lip',*lip_outer,SEAM-P['lip_depth'],SEAM+.15,2.2)
boolean(lip,rounded('lip_void',lip_outer[0]-2*P['lip_wall'],lip_outer[1]-2*P['lip_wall'],SEAM-P['lip_depth']-1,SEAM+1,.7))
for x,y in SCREWS:
    boolean(lip,cylinder('boss_relief',5.0,SEAM-P['lip_depth']-1,SEAM+1,x,y))
union(lid,lip)
boolean(lid,rounded('window',P['window_width'],P['window_height'],SEAM-4,DEPTH+1,.6,WX,WY))
# Mouth expands by 1.6 mm per side above a short vertical throat.
bottom=rounded_profile(P['window_width'],P['window_height'],.6,WX,WY)
top=rounded_profile(P['window_width']+3.2,P['window_height']+3.2,2.2,WX,WY)
n=len(bottom)
verts=[(x,y,DEPTH-1.6) for x,y in bottom]+[(x,y,DEPTH+.01) for x,y in top]
faces=[tuple(reversed(range(n))),tuple(range(n,2*n))]+[(i,(i+1)%n,(i+1)%n+n,i+n) for i in range(n)]
boolean(lid,mesh('window_chamfer',verts,faces))
for x,y in SCREWS:
    boolean(lid,cylinder('clearance',P['case_screw_clearance']/2,SEAM-4,DEPTH+1,x,y))
    boolean(lid,cylinder('head_recess',3.25,DEPTH-2,DEPTH+1,x,y))
paint(lid,lid_mat)

# Desk cradle. Flat base, two open gussets, full-width bottom seat and front stop.
ANGLE=math.radians(P['desk_angle'])
R=Matrix.Rotation(ANGLE,4,'X')
Z_LIFT=P['stand_bottom_lift']+H/2*math.sin(ANGLE)
ASSEMBLY=Matrix.Translation((0,0,Z_LIFT)) @ R
USB_RIGHT = P['usb_on_right'] >= .5
CASE_ROTATION = Matrix.Rotation(math.pi if USB_RIGHT else 0,4,'Z')
CASE_ASSEMBLY = ASSEMBLY @ CASE_ROTATION
back_y=-H/2*math.cos(ANGLE)
top_y=(H/2-8)*math.cos(ANGLE)
top_z=(H/2-8)*math.sin(ANGLE)+Z_LIFT
base_y=-3
front_y=base_y-P['stand_depth']/2
rear_y=base_y+P['stand_depth']/2
# Keep the seat inside the footprint as angle or stack depth changes.
seat_back=ASSEMBLY @ Vector((0,-H/2-4,-3))
seat_front=ASSEMBLY @ Vector((0,-H/2-4,DEPTH+2))
front_y=min(front_y,seat_front.y-2)
base_y=(front_y+rear_y)/2
stand=rounded('03_DESK_CRADLE',P['stand_width'],rear_y-front_y,0,P['stand_floor'],6,0,base_y)

def side_prism(name, profile, x0, x1):
    o=prism(name,profile,x0,x1)
    # prism (profile_y, profile_z, thickness_x) -> world (x,y,z)
    o.data.transform(Matrix(((0,0,1,0),(1,0,0,0),(0,1,0,0),(0,0,0,1))))
    normals(o)
    return o

# Continuous front plinth supports the full seat during base-down printing.
plinth_profile=[(seat_front.y,P['stand_floor']-.2),(seat_back.y,P['stand_floor']-.2),
                (seat_back.y,seat_back.z+.25),(seat_front.y,seat_front.z+.25)]
union(stand,side_prism('seat_plinth',plinth_profile,-P['stand_ledge_width']/2,P['stand_ledge_width']/2))

for x in (-P['stand_rib_spacing']/2,P['stand_rib_spacing']/2):
    profile=[(back_y-7,P['stand_floor']-.2),(back_y,P['stand_bottom_lift']),
             (top_y,top_z),(rear_y-5,P['stand_floor']-.2)]
    rib=side_prism('gusset',profile,x-P['stand_rib_width']/2,x+P['stand_rib_width']/2)
    # Large triangular relief stays at least 6 mm from the supporting face.
    hole=[(back_y+10,P['stand_floor']+6),(top_y,top_z-17),(rear_y-13,P['stand_floor']+6)]
    boolean(rib,side_prism('gusset_window',hole,x-P['stand_rib_width'],x+P['stand_rib_width']))
    union(stand,rib)
seat=box('seat',(P['stand_ledge_width'],4.0,DEPTH+5.0),(0,-H/2-2,DEPTH/2-.5))
seat.matrix_world=ASSEMBLY @ seat.matrix_world
union(stand,seat)
stop=box('front_stop',(P['stand_ledge_width'],7,2.4),(0,-H/2+.6,DEPTH+P['stand_fit_clearance']+1.2))
stop.matrix_world=ASSEMBLY @ stop.matrix_world
union(stand,stop)
for x in (-P['stand_width']/2+10,P['stand_width']/2-10):
    for y in (front_y+10,rear_y-10):
        boolean(stand,cylinder('rubber_foot_recess',P['foot_diameter']/2,-1,P['foot_recess'],x,y))
paint(stand,stand_mat)

# Low-filament fit gauges: PCB-hole template and viewing-aperture ring.
gauge=rounded('04_BOARD_FIT_GAUGE',108,60,0,1.6,3)
boolean(gauge,rounded('lightweight_cutout',96,36,-1,2,2))
for x,y in HOLES:
    boolean(gauge,cylinder('mount_hole',1.7,-1,3,x,y))
paint(gauge,lid_mat)
screen_gauge=rounded('05_WINDOW_FIT_GAUGE',P['shield_width']+4,P['shield_height']+4,0,1.6,1.6,SX,SY)
boolean(screen_gauge,rounded('aperture',P['window_width'],P['window_height'],-1,3,.6,WX,WY))
paint(screen_gauge,lid_mat)

parts=[tray,lid,stand,gauge,screen_gauge]

def validate(obj):
    bm=bmesh.new(); bm.from_mesh(obj.data)
    # Boolean seams can leave collinear vertices which tessellate into slivers.
    # Clean the print mesh before testing/exporting the actual triangles.
    bmesh.ops.dissolve_limit(bm,angle_limit=.0001,use_dissolve_boundaries=True,
                            verts=list(bm.verts),edges=list(bm.edges))
    bmesh.ops.triangulate(bm,faces=list(bm.faces))
    bmesh.ops.dissolve_degenerate(bm,dist=.00001,edges=list(bm.edges))
    bmesh.ops.triangulate(bm,faces=list(bm.faces))
    bmesh.ops.recalc_face_normals(bm,faces=list(bm.faces))
    nonmanifold=sum(not e.is_manifold for e in bm.edges)
    loose=sum(not v.link_faces for v in bm.verts)
    degenerate=sum(f.calc_area()<1e-8 for f in bm.faces)
    seen=set(); components=0
    for v in bm.verts:
        if v in seen: continue
        components+=1; todo=[v]; seen.add(v)
        while todo:
            u=todo.pop()
            for e in u.link_edges:
                q=e.other_vert(u)
                if q not in seen: seen.add(q); todo.append(q)
    volume=bm.calc_volume(signed=True)
    min_area=min(f.calc_area() for f in bm.faces)
    bm.to_mesh(obj.data); bm.free()
    result=dict(nonmanifold_edges=nonmanifold,loose_vertices=loose,degenerate_faces=degenerate,connected_components=components,volume_mm3=round(volume,2),minimum_triangle_area_mm2=min_area)
    assert nonmanifold==0 and loose==0 and degenerate==0 and components==1 and volume>0, (obj.name,result)
    return result

def stl(obj,filename,transform=None):
    # Explicit binary STL avoids scene-unit/exporter ambiguities.
    mat=transform if transform is not None else obj.matrix_world
    me=obj.data; me.calc_loop_triangles()
    tris=me.loop_triangles
    allv=[mat @ v.co for v in me.vertices]
    low=min(v.z for v in allv)
    with open(OUT/filename,'wb') as f:
        f.write(b'TFT DESK | millimetres | fit-check prototype'.ljust(80,b' '))
        f.write(struct.pack('<I',len(tris)))
        for tri in tris:
            vs=[allv[i].copy() for i in tri.vertices]
            for v in vs: v.z-=low
            normal=(vs[1]-vs[0]).cross(vs[2]-vs[0]).normalized()
            f.write(struct.pack('<12fH',*normal,*vs[0],*vs[1],*vs[2],0))
    mins=[min(v[i] for v in allv) for i in range(3)]
    maxs=[max(v[i] for v in allv) for i in range(3)]
    return [round(b-a,3) for a,b in zip(mins,maxs)]

report={'units':'mm','usb_side_from_front':'right' if USB_RIGHT else 'left','fit_status':'PROVISIONAL: verify actual shield XY, registration, stack height and cable plugs',
        'parameters':P,'derived':{'case_depth':DEPTH,'pcb_underside_z':PCB_Z,'glass_z':GLASS_Z,'lid_seam_z':SEAM},'parts':{}}
for obj,name,transform in [(tray,'01_rear_tray.stl',None),(lid,'02_front_bezel.stl',Matrix.Rotation(math.pi,4,'X')),
        (stand,'03_desk_cradle.stl',None),
        (gauge,'04_board_fit_gauge.stl',None),(screen_gauge,'05_window_fit_gauge.stl',None)]:
    report['parts'][name]=validate(obj)
    report['parts'][name]['print_bounds_mm']=stl(obj,name,transform)
    obj['export_file']=name
    obj['units']='millimetres'
    obj['fit_status']='Prototype — physical fit not yet verified'

# Print scene uses the exact STL orientations. Gauges are separate optional parts.
print_scene=bpy.data.scenes.new(PREFIX+'PRINT_LAYOUT')
print_scene.unit_settings.system='METRIC'; print_scene.unit_settings.scale_length=.001
for obj,loc,rot in [(tray,(-70,0,0),Matrix.Identity(4)),(lid,(70,0,0),Matrix.Rotation(math.pi,4,'X')),
        (stand,(0,125,0),Matrix.Identity(4)),
        (gauge,(-70,-95,0),Matrix.Identity(4)),(screen_gauge,(65,-95,0),Matrix.Identity(4))]:
    copy=obj.copy(); copy.data=obj.data.copy(); print_scene.collection.objects.link(copy)
    copy.name=obj.name+'_PRINT'
    copy.matrix_world=rot
    minz=min((copy.matrix_world @ Vector(v)).z for v in copy.bound_box)
    copy.location+=Vector((loc[0],loc[1],-minz))
gauge.hide_render=True; gauge.hide_set(True)
screen_gauge.hide_render=True; screen_gauge.hide_set(True)

# Hardware references use Arduino's original Eagle outline and through-holes.
current_coll=refs_coll
eagle=ET.fromstring(zipfile.ZipFile(ROOT/'references/MEGA2560_Rev3e.zip').read('MEGA2560_Rev3e.brd'))
origin=Vector((P['board_origin_x'],P['board_origin_y'],0))
outline=[(-.0256,.9872),(.9744,-.0128),(98.0344,-.0128),(99.0344,.9872),
         (99.0344,2.5272),(101.5744,5.0672),(101.5744,37.8332),(99.0344,40.3732),
         (99.0344,51.8032),(97.5104,53.3272),(.9744,53.3272),(-.0256,52.3272)]
board=prism('REF_MEGA_PCB',[(x+origin.x,y+origin.y) for x,y in outline],PCB_Z,PCB_Z+1.6)
for h in eagle.findall('.//board/plain/hole'):
    boolean(board,cylinder('hole',float(h.get('drill'))/2,PCB_Z-1,PCB_Z+3,float(h.get('x'))+origin.x,float(h.get('y'))+origin.y))
paint(board,pcb_mat)
for element in eagle.findall('.//board/elements/element'):
    pkg=element.get('package')
    if pkg in ('1X08','1X10','2X18','2X03'):
        nx,ny=(2.54*(int(pkg[2:]))+.5,2.8) if pkg.startswith('1X') else ((7.8,5.3) if pkg=='2X03' else (46.2,5.3))
        h=8.5 if pkg.startswith('1X') or pkg=='2X18' else 8.0
        ob=box('REF_HEADER_'+element.get('name'),(nx,ny,h),(float(element.get('x'))+origin.x,float(element.get('y'))+origin.y,PCB_Z+1.6+h/2))
        ob.rotation_euler.z=math.radians(float(element.get('rot','R0').replace('R','')))
        paint(ob,black_mat)
usb=box('REF_USB_B_SOCKET',(15.75,11.8,11.0),(1.535+origin.x,38.1+origin.y,PCB_Z+7.1))
paint(usb,silver_mat)
dc=box('REF_DC_SOCKET',(13.4874,8.9916,11.0),(4.9403+origin.x,7.7978+origin.y,PCB_Z+7.1))
paint(dc,black_mat)
paint(box('REF_MEGA_PROCESSOR',(14,14,1.5),(0,0,PCB_Z+2.35)),black_mat)
shield=rounded('REF_SHIELD_ENVELOPE',P['shield_width'],P['shield_height'],GLASS_Z-5.4,GLASS_Z-3.8,.5,SX,SY)
paint(shield,pcb_mat)
paint(rounded('REF_LCD_FRAME',P['shield_width']-.8,P['shield_height']-.8,GLASS_Z-3.7,GLASS_Z-.05,.6,SX,SY),silver_mat)
paint(rounded('REF_GLASS',P['shield_width']-2,P['shield_height']-2,GLASS_Z-.15,GLASS_Z,.3,SX,SY),glass_mat)

# Illustrative dashboard; these objects never enter STL exports.
def label(name,text,x,y,z,size,mat):
    curve=bpy.data.curves.new(PREFIX+name,'FONT'); curve.body=text; curve.size=size
    curve.extrude=0; curve.align_x='CENTER'
    ob=bpy.data.objects.new(PREFIX+name,curve); current_coll.objects.link(ob)
    ob.location=(x,y,z); curve.materials.append(mat)
    return ob

label('REF_UI_HEADING','SYSTEM / LIVE',WX,WY+19,GLASS_Z+.03,2.3,white_mat)
for i,(title,value) in enumerate([('CPU','24%'),('RAM','47%'),('GPU','08%')]):
    x=WX+(i-1)*23
    for j in range(28):
        a=math.radians(-35+j*250/27)
        ob=box('REF_GAUGE',(1.8,.65,.03),(x+7.5*math.cos(a),WY+4+7.5*math.sin(a),GLASS_Z+.03))
        ob.rotation_euler.z=a
        paint(ob,cyan_mat if j<12-i*3 else muted_mat)
    label('REF_UI_VALUE',value,x,WY+3,GLASS_Z+.06,3.2,white_mat)
    label('REF_UI_METRIC',title,x,WY-7,GLASS_Z+.03,2,white_mat)
for j in range(3):
    paint(box('REF_UI_BAR',(64,1.4,.04),(WX,WY-13-j*3,GLASS_Z+.04)),muted_mat)
    paint(box('REF_UI_BAR_FILL',(44-j*12,1.4,.06),(WX-10-j*6,WY-13-j*3,GLASS_Z+.06)),cyan_mat)

# Case fasteners are shown installed; they are hardware, never printable parts.
for x,y in SCREWS:
    head=cylinder('REF_M3_BUTTON_HEAD',2.85,DEPTH-2,DEPTH-.35,x,y)
    boolean(head,cylinder('hex_drive',1.25,DEPTH-1.3,DEPTH+.1,x,y,n=6))
    paint(head,silver_mat)

# Clearance checks use reference envelopes and actual solid intersections.
def intersection_volume(a,b):
    duplicate=a.copy(); duplicate.data=a.data.copy(); current_coll.objects.link(duplicate)
    tool=b.copy(); tool.data=b.data.copy(); current_coll.objects.link(tool)
    boolean(duplicate,tool,'INTERSECT')
    bm=bmesh.new(); bm.from_mesh(duplicate.data)
    vol=abs(bm.calc_volume()); bm.free()
    bpy.data.objects.remove(duplicate,do_unlink=True)
    return round(vol,5)

checks={}
for part in (tray,lid):
    for hardware in (board,usb,dc,shield):
        v=intersection_volume(part,hardware)
        checks[part.name+' vs '+hardware.name]=v
        assert v<.01, ('Hardware collision',part.name,hardware.name,v)
checks['tray vs lid']=intersection_volume(tray,lid)
assert checks['tray vs lid']<.01
report['reference_intersection_volume_mm3']=checks

# Assemble case and hardware, keep cradle on the desk. Refs are hidden in print scene.
for obj in list(refs_coll.objects)+[tray,lid]:
    if USB_RIGHT and ('REF_UI_' in obj.name or 'REF_GAUGE' in obj.name):
        # Keep illustrative firmware upright after rotating the physical stack.
        obj.matrix_world=ASSEMBLY @ Matrix.Translation((-2*WX,-2*WY,0)) @ obj.matrix_world
    else:
        obj.matrix_world=CASE_ASSEMBLY @ obj.matrix_world
for obj in refs_coll.objects:
    obj['reference_only']=True
    obj['dimensional_status']='Arduino XY sourced from CAD; shield and vertical envelopes provisional'

checks['assembled tray vs cradle']=intersection_volume(tray,stand)
checks['assembled lid vs cradle']=intersection_volume(lid,stand)
assert checks['assembled tray vs cradle']<.01 and checks['assembled lid vs cradle']<.01, checks

current_coll=control_coll
control=bpy.data.objects.new(PREFIX+'PARAMS',None); control_coll.objects.link(control)
control.hide_render=True
control['HOW_TO_EDIT']='Change values here, then run the embedded build_enclosure.py text (Alt-P in Text Editor). Units: mm, angle: degrees.'
control['FIT_STATUS']='Physical shield fit pending. Measure stack_height and shield registration.'
for key,value in P.items():
    if isinstance(value,(float,int)):
        control[key]=value
        control.id_properties_ui(key).update(description=('Degrees above desktop' if key=='desk_angle' else 'Millimetres; regenerate using embedded build_enclosure.py'))

current_coll=studio_coll
paint(rounded('STUDIO_GROUND',2000,2000,-2,-1.2,30),floor_mat)
# Real adhesive feet: 2 mm tall, seated 0.8 mm into the recess.
for x in (-P['stand_width']/2+10,P['stand_width']/2-10):
    for y in (front_y+10,rear_y-10):
        paint(cylinder('REF_ADHESIVE_FOOT',5,-1.2,.8,x,y),black_mat)

def aim(obj,target):
    obj.rotation_euler=(Vector(target)-obj.location).to_track_quat('-Z','Y').to_euler()

cam_data=bpy.data.cameras.new(PREFIX+'CAMERA')
cam=bpy.data.objects.new(PREFIX+'CAMERA',cam_data); studio_coll.objects.link(cam)
cam.location=(185 if USB_RIGHT else -185,-250,175); aim(cam,(0,0,43)); cam_data.type='ORTHO'; cam_data.ortho_scale=215
cam_data.clip_end=5000; scene.camera=cam
for name,pos,power,size in [('KEY',(-180,-150,280),1800000,180),('FILL',(180,-80,140),1100000,150),('RIM',(40,170,250),2200000,130)]:
    ld=bpy.data.lights.new(PREFIX+name,'AREA'); ld.energy=power; ld.shape='DISK'; ld.size=size
    lo=bpy.data.objects.new(PREFIX+name,ld); studio_coll.objects.link(lo); lo.location=pos; aim(lo,(0,0,30))
scene.world=bpy.data.worlds.new(PREFIX+'WORLD')
scene.world.use_nodes=True
next(n for n in scene.world.node_tree.nodes if n.type=='BACKGROUND').inputs[0].default_value=(.22,.26,.32,1)
next(n for n in scene.world.node_tree.nodes if n.type=='BACKGROUND').inputs[1].default_value=.35
try:
    scene.render.engine='CYCLES'
    scene.cycles.samples=32
    scene.cycles.use_denoising=True
except TypeError:
    pass
scene.render.resolution_x=1200; scene.render.resolution_y=1000; scene.render.resolution_percentage=100
scene.render.image_settings.file_format='PNG'
scene.render.filepath='//assembled.png'
# Do not embed workstation paths or render-session details into shared images.
for setting in scene.render.bl_rna.properties:
    if setting.identifier.startswith('use_stamp') and setting.type == 'BOOLEAN':
        setattr(scene.render, setting.identifier, False)
scene.render.film_transparent=False

# Exploded scene: assembled orientation with lid and electronics lifted for inspection.
exploded=bpy.data.scenes.new(PREFIX+'EXPLODED')
exploded.unit_settings.system='METRIC'; exploded.unit_settings.scale_length=.001
normal=R.to_3x3() @ Vector((0,0,1))
for obj in list(parts_coll.objects)+list(refs_coll.objects):
    if obj in (gauge,screen_gauge): continue
    copy=obj.copy(); exploded.collection.objects.link(copy); copy.name=obj.name+'_EXPLODED'
    if obj==lid or 'REF_M3_' in obj.name: copy.location+=normal*58
    elif obj in refs_coll.objects[:]: copy.location+=normal*28

# Persist inspectable state, exact build inputs, and source embedded in the blend.
(OUT/'validation.json').write_text(json.dumps(report,indent=2))
(OUT/'parameters_used.json').write_text(json.dumps(P,indent=2))
source=ROOT/'build_enclosure.py'
if source.exists():
    text=bpy.data.texts.get('build_enclosure.py') or bpy.data.texts.new('build_enclosure.py')
    text.clear(); text.write(source.read_text())
readme=ROOT/'README.md'
if readme.exists():
    text=bpy.data.texts.get('START_HERE.md') or bpy.data.texts.new('START_HERE.md')
    text.clear(); text.write(readme.read_text())
activate(control)
if bpy.context.screen:
    for area in bpy.context.screen.areas:
        if area.type=='VIEW_3D':
            area.spaces.active.region_3d.view_perspective='CAMERA'
            area.spaces.active.clip_end=10000
            area.spaces.active.shading.color_type='MATERIAL'
# Blender's default Shading workspace stores the user's home in its file browser.
# Shared projects use relative browser locations instead.
for screen in bpy.data.screens:
    for area in screen.areas:
        for space in area.spaces:
            if space.type == 'FILE_BROWSER' and space.params is not None:
                space.params.directory = b'//'
                space.params.filename = ''
for generated in OUT.iterdir():
    os.replace(generated,FINAL_OUT/generated.name)
_build_dir.cleanup()
bpy.ops.wm.save_as_mainfile(filepath=str(FINAL_OUT/'tft_desk_parametric.blend'), compress=False)
print(json.dumps({'saved':str(FINAL_OUT/'tft_desk_parametric.blend'),'case_mm':[W,H,DEPTH], 'validation':report['parts'],'clearance_checks':checks},indent=2))
