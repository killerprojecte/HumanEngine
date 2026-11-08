#define _GNU_SOURCE
// Opaque public libvips C ABI. No private layouts, C++ ABI, Node or headers
// are required. Signatures follow libvips 8.18.7 public headers.
#include "bridge.h"
#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <sys/mman.h>
#include <fcntl.h>
#include <unistd.h>
#include <errno.h>
#include <stdatomic.h>
// Snapshot into a sealed anonymous file before hashing/loading. The verified
// inode cannot be replaced or modified between Go verification and dlopen.
int he_seal_library(int source_fd) {
 int fd=memfd_create("humanengine-libvips",MFD_CLOEXEC|MFD_ALLOW_SEALING);if(fd<0)return -1;
 char buf[65536];ssize_t n;
 while((n=read(source_fd,buf,sizeof(buf)))!=0){
  if(n<0){if(errno==EINTR)continue;goto fail;}
  ssize_t off=0;while(off<n){ssize_t wrote=write(fd,buf+off,n-off);if(wrote<0){if(errno==EINTR)continue;goto fail;}off+=wrote;}
 }
 if(fcntl(fd,F_ADD_SEALS,F_SEAL_WRITE|F_SEAL_GROW|F_SEAL_SHRINK|F_SEAL_SEAL)<0||lseek(fd,0,SEEK_SET)<0)goto fail;
 return fd;
 fail:{int saved=errno;close(fd);errno=saved;return -1;}
}
static _Atomic long live_inputs;
long he_live_inputs(void){return atomic_load(&live_inputs);}
typedef struct { _Atomic long refs; unsigned char bytes[]; } OwnedSource;
void *he_source_new(const void *data,size_t n){
 OwnedSource *s=malloc(sizeof(*s)+n);if(!s)return NULL;atomic_init(&s->refs,1);memcpy(s->bytes,data,n);atomic_fetch_add(&live_inputs,1);return s->bytes;
}
static OwnedSource *source_owner(const void *p){return (OwnedSource*)((char*)p-sizeof(OwnedSource));}
void he_source_free(void *p){if(p&&atomic_fetch_sub(&source_owner(p)->refs,1)==1){free(source_owner(p));atomic_fetch_sub(&live_inputs,1);}}
static void retain_input(const void *p){atomic_fetch_add(&source_owner(p)->refs,1);}

typedef void VipsImage;
#define FN(ret,name,args) static ret (*name) args
FN(int,vips_init,(const char*));
FN(void,vips_cache_set_max,(int));
FN(void,vips_cache_set_max_mem,(size_t));
FN(void,vips_cache_set_max_files,(int));
FN(void,g_object_set_data_full,(void*,const char*,void*,void (*)(void*)));
FN(const char*,vips_error_buffer,(void));
FN(char*,vips_error_buffer_copy,(void));
FN(void,vips_thread_shutdown,(void));
FN(void,vips_concurrency_set,(int));
FN(void,vips_error_clear,(void));
FN(const char*,vips_foreign_find_load_buffer,(const void*,size_t));
FN(VipsImage*,vips_image_new_from_buffer,(const void*,size_t,const char*,...));
FN(int,vips_jpegload_buffer,(void*,size_t,VipsImage**,...));
FN(int,vips_image_get_width,(VipsImage*)); FN(int,vips_image_get_height,(VipsImage*));
FN(int,vips_image_get_orientation,(VipsImage*)); FN(int,vips_image_hasalpha,(VipsImage*));
FN(int,vips_image_get_int,(VipsImage*,const char*,int*));
FN(unsigned long,vips_image_get_typeof,(VipsImage*,const char*));
FN(int,vips_image_get_format,(VipsImage*)); FN(int,vips_image_get_interpretation,(VipsImage*));
FN(int,vips_stats,(VipsImage*,VipsImage**,...));
FN(int,vips_image_get_bands,(VipsImage*));
FN(VipsImage*,vips_image_copy_memory,(VipsImage*));
FN(VipsImage*,vips_image_new_matrixv,(int,int,...));
FN(void,vips_image_set_double,(VipsImage*,const char*,double));
FN(int,vips_extract_band,(VipsImage*,VipsImage**,int,...));
FN(int,vips_hist_find,(VipsImage*,VipsImage**,...));
FN(int,vips_hist_entropy,(VipsImage*,double*,...));
FN(int,vips_conv,(VipsImage*,VipsImage**,VipsImage*,...));
FN(int,vips_deviate,(VipsImage*,double*,...));
FN(int,vips_hist_find_ndim,(VipsImage*,VipsImage**,...));
FN(int,vips_max,(VipsImage*,double*,...));

FN(int,vips_resize,(VipsImage*,VipsImage**,double,...));
FN(int,vips_premultiply,(VipsImage*,VipsImage**,...)); FN(int,vips_unpremultiply,(VipsImage*,VipsImage**,...));
FN(int,vips_cast,(VipsImage*,VipsImage**,int,...)); FN(int,vips_rot,(VipsImage*,VipsImage**,int,...));
FN(int,vips_flip,(VipsImage*,VipsImage**,int,...));
FN(int,vips_colourspace,(VipsImage*,VipsImage**,int,...));
FN(int,vips_icc_transform,(VipsImage*,VipsImage**,const char*,...));
FN(int,vips_pngsave_buffer,(VipsImage*,void**,size_t*,...)); FN(int,vips_jpegsave_buffer,(VipsImage*,void**,size_t*,...));
FN(int,vips_webpsave_buffer,(VipsImage*,void**,size_t*,...));
FN(void,g_object_unref,(void*)); FN(void,g_free,(void*));
static char init_error[256];
#define LOAD(n) do { *(void **)(&n)=dlsym(lib,#n); if(!n){snprintf(init_error,sizeof(init_error),"missing public ABI symbol: %s",#n);dlclose(lib);return -1;} } while(0)
int he_init(const char *path) {
 init_error[0]=0;
 void *lib=dlopen(path,RTLD_NOW|RTLD_LOCAL); if(!lib){snprintf(init_error,sizeof(init_error),"%s",dlerror());return -1;}
 LOAD(vips_init); LOAD(vips_cache_set_max); LOAD(vips_cache_set_max_mem); LOAD(vips_cache_set_max_files); LOAD(g_object_set_data_full); LOAD(vips_error_buffer); LOAD(vips_error_buffer_copy); LOAD(vips_thread_shutdown); LOAD(vips_concurrency_set); LOAD(vips_error_clear); LOAD(vips_foreign_find_load_buffer); LOAD(vips_image_new_from_buffer); LOAD(vips_jpegload_buffer);
 LOAD(vips_image_get_width); LOAD(vips_image_get_height); LOAD(vips_image_get_orientation); LOAD(vips_image_hasalpha); LOAD(vips_image_get_int); LOAD(vips_image_get_typeof); LOAD(vips_image_get_format); LOAD(vips_image_get_interpretation);
 LOAD(vips_image_get_bands); LOAD(vips_image_copy_memory); LOAD(vips_image_new_matrixv); LOAD(vips_image_set_double); LOAD(vips_extract_band); LOAD(vips_hist_find); LOAD(vips_hist_entropy); LOAD(vips_conv); LOAD(vips_deviate); LOAD(vips_hist_find_ndim); LOAD(vips_max); LOAD(vips_stats); LOAD(vips_resize); LOAD(vips_premultiply); LOAD(vips_unpremultiply); LOAD(vips_cast); LOAD(vips_rot); LOAD(vips_flip); LOAD(vips_colourspace); LOAD(vips_icc_transform); LOAD(vips_webpsave_buffer); LOAD(vips_pngsave_buffer); LOAD(vips_jpegsave_buffer); LOAD(g_object_unref); LOAD(g_free);
 int rc=vips_init("humanengine-imagecodec");
 if(rc){snprintf(init_error,sizeof(init_error),"%s",vips_error_buffer());dlclose(lib);return rc;}
 // Actual sharp/dist/utility.cjs cache(true), not an additional admission cap.
 vips_cache_set_max_mem(50*1024*1024);vips_cache_set_max_files(20);vips_cache_set_max(100);
 // Same glibc allocator branch as sharp/dist/utility.cjs, not a new cap.
 const char *arenas=getenv("MALLOC_ARENA_MAX");
 if((!arenas||!arenas[0])&&!dlsym(RTLD_DEFAULT,"mallctl"))vips_concurrency_set(1);
 return 0;
}
const char *he_error(void) { return init_error[0]?init_error:vips_error_buffer(); }
void he_free(void *p) { g_free(p); }
void he_trim_cache_for_test(void){vips_cache_set_max(0);vips_cache_set_max(100);}
static int field(VipsImage *im,const char *key,int fallback) {int n=fallback;if(vips_image_get_typeof(im,key))vips_image_get_int(im,key,&n);return n;}
static const char *format(const void *data,size_t n) {
 const char *loader=vips_foreign_find_load_buffer(data,n); if(!loader)return NULL;
 if(!strcmp(loader,"VipsForeignLoadPngBuffer"))return "png"; if(!strcmp(loader,"VipsForeignLoadJpegBuffer")||!strcmp(loader,"VipsForeignLoadUhdr")||!strcmp(loader,"VipsForeignLoadUhdrBuffer"))return "jpeg"; if(!strcmp(loader,"VipsForeignLoadGifBuffer")||!strcmp(loader,"VipsForeignLoadNsgifBuffer"))return "gif"; if(!strcmp(loader,"VipsForeignLoadWebpBuffer"))return "webp";return NULL;
}
static VipsImage *open_image(const void *data,size_t n,const char *fmt,int shrink,double scale) {
 // Share one encoded allocation across every resize/search pipeline and cache
 // entry. Each image owns a reference, not another full source-byte copy.
 void *owned=(void*)data;retain_input(owned);VipsImage *im;
 // VIPS_ACCESS_SEQUENTIAL=1, VIPS_FAIL_ON_WARNING=3.
 if(!strcmp(fmt,"gif"))im=vips_image_new_from_buffer(owned,n,"","access",1,"fail_on",3,"n",1,"page",0,NULL);
 else if(!strcmp(fmt,"webp"))im=vips_image_new_from_buffer(owned,n,"","access",1,"fail_on",3,"n",1,"page",0,"scale",scale,NULL);
 else if(!strcmp(fmt,"jpeg")&&shrink>1){
  // Sharp initial OpenInput auto-detects UHDR as JPEG without a shrink option.
  // Its preload branch deliberately calls jpegload_buffer, bypassing UHDR.
  im=NULL; if(vips_jpegload_buffer(owned,n,&im,"access",1,"fail_on",3,"shrink",shrink,NULL)){if(im)g_object_unref(im);im=NULL;}
 }
 else im=vips_image_new_from_buffer(owned,n,"","access",1,"fail_on",3,NULL);
 if(im)g_object_set_data_full(im,"humanengine-owned-input",owned,he_source_free);else he_source_free(owned);
 return im;
}
// Decode and exercise the same secondary operations as sharp.stats, including
// colour conversion errors that a header-only or plain raw decode would miss.
static int full_stats(VipsImage *source) {
 VipsImage *im=vips_image_copy_memory(source),*stats=NULL,*grey=NULL,*band=NULL,*hist=NULL,*mask=NULL,*conv=NULL,*rgb=NULL,*nd=NULL,*noalpha=NULL;
 int rc=-1;double d;if(!im)return -1;
 if(vips_stats(im,&stats,NULL)||vips_colourspace(im,&grey,1,NULL)||vips_extract_band(grey,&band,0,NULL)||vips_hist_find(band,&hist,NULL)||vips_hist_entropy(hist,&d,NULL))goto done;
 if(vips_image_get_width(im)>1||vips_image_get_height(im)>1){
  mask=vips_image_new_matrixv(3,3,0.0,1.0,0.0,1.0,-4.0,1.0,0.0,1.0,0.0);if(!mask)goto done;
  vips_image_set_double(mask,"scale",9.0);if(vips_conv(band,&conv,mask,NULL)||vips_deviate(conv,&d,NULL))goto done;
 }
 if(vips_image_hasalpha(im)){if(vips_extract_band(im,&noalpha,0,"n",vips_image_get_bands(im)-1,NULL))goto done;}
 if(vips_colourspace(noalpha?noalpha:im,&rgb,22,NULL)||vips_hist_find_ndim(rgb,&nd,"bins",16,NULL)||vips_max(nd,&d,NULL))goto done;
 rc=0;
 done:;
 VipsImage *all[]={im,stats,grey,band,hist,mask,conv,rgb,nd,noalpha};for(unsigned i=0;i<sizeof(all)/sizeof(all[0]);i++)if(all[i])g_object_unref(all[i]);return rc;
}
static int inspect_image(const void *data,size_t n,he_metadata *m) {
 const char *fmt=format(data,n);if(!fmt)return -1;
 VipsImage *im=open_image(data,n,fmt,1,1);if(!im)return -1;
 m->width=vips_image_get_width(im);m->height=vips_image_get_height(im);
 m->pages=field(im,"n-pages",1);m->page_height=field(im,"page-height",m->height);
 m->orientation=vips_image_get_orientation(im);m->alpha=vips_image_hasalpha(im);strcpy(m->format,fmt);
 int rc=-1;
 if(m->width>0&&m->height>0&&(uint64_t)m->width*m->height<=40000000)rc=full_stats(im);
 g_object_unref(im);return rc;
}
#define STEP(call) do {VipsImage *next=NULL;if((call)){if(next)g_object_unref(next);goto fail;}g_object_unref(im);im=next;}while(0)
static int render_image(const void *data,size_t n,int width,int height,int quality,void **out,size_t *out_len) {
 const char *fmt=format(data,n);if(!fmt)return -1;
 VipsImage *im=open_image(data,n,fmt,1,1);if(!im)return -1;
 int orientation=vips_image_get_orientation(im),w=vips_image_get_width(im),h=vips_image_get_height(im);
 if(orientation>=5){int t=width;width=height;height=t;}
 double shrink=fmax(1,fmax((double)w/width,(double)h/height));
 // ResolveShrink clamps each axis before selecting common decoder reduction.
 shrink=fmin(fmin(shrink,w),fmin(shrink,h));
 int jpeg_shrink=shrink>=8?8:shrink>=4?4:shrink>=2?2:1;
 if(jpeg_shrink>1&&(int)shrink==jpeg_shrink)jpeg_shrink/=2;
 if((!strcmp(fmt,"jpeg")&&jpeg_shrink>1)||(!strcmp(fmt,"webp")&&shrink>1)){
  g_object_unref(im);im=open_image(data,n,fmt,jpeg_shrink,!strcmp(fmt,"webp")?1/shrink:1);if(!im)return -1;
 }
 w=vips_image_get_width(im);h=vips_image_get_height(im);
 shrink=fmax(1,fmax((double)w/width,(double)h/height));
 double hs=fmin(shrink,w),vs=fmin(shrink,h);
 int interp=vips_image_get_interpretation(im);
 const char *processing_profile=interp==25?"p3":"srgb";
 if(vips_image_get_typeof(im,"icc-profile-data")&&interp!=21&&interp!=26){
  VipsImage *next=NULL;
  if(!vips_icc_transform(im,&next,processing_profile,"embedded",1,"depth",(interp==25||interp==26)?16:8,"intent",0,NULL)){g_object_unref(im);im=next;}else{if(next)g_object_unref(next);vips_error_clear();}
 }else if(interp==15){STEP(vips_icc_transform(im,&next,processing_profile,"input_profile","cmyk","intent",0,NULL));}
 int band_format=vips_image_get_format(im),premul=vips_image_hasalpha(im)&&(hs!=1||vs!=1);
 if(premul){STEP(vips_premultiply(im,&next,NULL));STEP(vips_cast(im,&next,band_format,NULL));}
 if(hs!=1||vs!=1){STEP(vips_resize(im,&next,1/hs,"vscale",1/vs,"kernel",5,NULL));}
 int angle=orientation==6||orientation==5?1:orientation==3||orientation==4?2:orientation==8||orientation==7?3:0;
 if(angle){VipsImage *memory=vips_image_copy_memory(im);if(!memory)goto fail;g_object_unref(im);im=memory;STEP(vips_rot(im,&next,angle,NULL));}
 if(orientation==2||orientation==4||orientation==5||orientation==7){STEP(vips_flip(im,&next,0,NULL));}
 if(premul){STEP(vips_unpremultiply(im,&next,NULL));STEP(vips_cast(im,&next,band_format,NULL));}
 interp=vips_image_get_interpretation(im);
 if(interp==25||interp==26){STEP(vips_cast(im,&next,2,NULL));}
 if(interp!=22){STEP(vips_colourspace(im,&next,22,"source_space",interp,NULL));}
 int rc= quality<0 ? vips_pngsave_buffer(im,out,out_len,"keep",0,"compression",9,"filter",8,"interlace",0,"palette",0,"Q",100,"effort",7,"bitdepth",8,"dither",1.0,NULL)
 :vips_jpegsave_buffer(im,out,out_len,"keep",0,"Q",quality,"interlace",1,"subsample_mode",1,"trellis_quant",1,"quant_table",3,"overshoot_deringing",1,"optimize_scans",1,"optimize_coding",1,NULL);
 g_object_unref(im);return rc;
 fail:g_object_unref(im);return -1;
}

// libvips owns a process-global error buffer. Atomic copy+clear prevents reading
// a mutable pointer. As in sharp/VError, simultaneous failing native operations
// can still combine/consume each other's diagnostics; no global pixel lock is
// added merely to conceal that upstream limitation.
int he_inspect(const void *data,size_t n,he_metadata *m){
 int rc=inspect_image(data,n,m);vips_error_clear();vips_thread_shutdown();return rc;
}
int he_render(const void *data,size_t n,int w,int h,int q,void **out,size_t *len,char **error){
 int rc=render_image(data,n,w,h,q,out,len);*error=rc?vips_error_buffer_copy():NULL;
 if(rc&&*out){g_free(*out);*out=NULL;*len=0;}
 if(!rc)vips_error_clear();vips_thread_shutdown();return rc;
}

// Meme intake uses sharp.metadata(), not stats(): do not force pixel decode.
int he_meme_metadata(const void *data,size_t n,he_metadata *m,char **error){
 int rc=-1;const char *fmt=format(data,n);VipsImage *im=NULL;
 if(fmt){im=open_image(data,n,fmt,1,1);if(im){
  m->width=vips_image_get_width(im);m->height=vips_image_get_height(im);
  m->pages=field(im,"n-pages",1);m->page_height=field(im,"page-height",m->height);
  m->orientation=vips_image_get_orientation(im);m->alpha=vips_image_hasalpha(im);strcpy(m->format,fmt);
  if(m->width>0&&m->height>0&&(uint64_t)m->width*m->height<=20000000)rc=0;
 }}
 if(im)g_object_unref(im);*error=rc?vips_error_buffer_copy():NULL;
 if(!rc)vips_error_clear();vips_thread_shutdown();return rc;
}


// Independent Meme page loader: analysis samples temporal pages and deliberately
// does NOT autorotate; thumbnail uses only page zero and autorotates later.
static VipsImage *open_meme_image(const void *data,size_t n,const char *fmt,int shrink,double scale,int page){
 if(strcmp(fmt,"gif")&&strcmp(fmt,"webp"))return open_image(data,n,fmt,shrink,scale);
 void *owned=(void*)data;retain_input(owned);VipsImage *im;
 if(!strcmp(fmt,"gif"))im=vips_image_new_from_buffer(owned,n,"","access",1,"fail_on",3,"n",1,"page",page,NULL);
 else im=vips_image_new_from_buffer(owned,n,"","access",1,"fail_on",3,"n",1,"page",page,"scale",scale,NULL);
 if(im)g_object_set_data_full(im,"humanengine-owned-input",owned,he_source_free);else he_source_free(owned);return im;
}
static int meme_render_image(const void *data,size_t n,int page,int thumbnail,void **out,size_t *out_len) {
 int width=thumbnail?320:512,height=width;
 const char *fmt=format(data,n);if(!fmt)return -1;
 VipsImage *im=open_meme_image(data,n,fmt,1,1,page);if(!im)return -1;
 int orientation=thumbnail?vips_image_get_orientation(im):0,w=vips_image_get_width(im),h=vips_image_get_height(im);
 if(orientation>=5){int t=width;width=height;height=t;}
 if(w<=0||h<=0||(uint64_t)w*h>20000000)goto fail;
 double shrink=fmax(1,fmax((double)w/width,(double)h/height));
 // ResolveShrink clamps each axis before selecting common decoder reduction.
 shrink=fmin(fmin(shrink,w),fmin(shrink,h));
 int jpeg_shrink=shrink>=8?8:shrink>=4?4:shrink>=2?2:1;
 if(jpeg_shrink>1&&(int)shrink==jpeg_shrink)jpeg_shrink/=2;
 if((!strcmp(fmt,"jpeg")&&jpeg_shrink>1)||(!strcmp(fmt,"webp")&&shrink>1)){
  g_object_unref(im);im=open_meme_image(data,n,fmt,jpeg_shrink,!strcmp(fmt,"webp")?1/shrink:1,page);if(!im)return -1;
 }
 w=vips_image_get_width(im);h=vips_image_get_height(im);
 shrink=fmax(1,fmax((double)w/width,(double)h/height));
 double hs=fmin(shrink,w),vs=fmin(shrink,h);
 int interp=vips_image_get_interpretation(im);
 const char *processing_profile=interp==25?"p3":"srgb";
 if(vips_image_get_typeof(im,"icc-profile-data")&&interp!=21&&interp!=26){
  VipsImage *next=NULL;
  if(!vips_icc_transform(im,&next,processing_profile,"embedded",1,"depth",(interp==25||interp==26)?16:8,"intent",0,NULL)){g_object_unref(im);im=next;}else{if(next)g_object_unref(next);vips_error_clear();}
 }else if(interp==15){STEP(vips_icc_transform(im,&next,processing_profile,"input_profile","cmyk","intent",0,NULL));}
 int band_format=vips_image_get_format(im),premul=vips_image_hasalpha(im)&&(hs!=1||vs!=1);
 if(premul){STEP(vips_premultiply(im,&next,NULL));STEP(vips_cast(im,&next,band_format,NULL));}
 if(hs!=1||vs!=1){STEP(vips_resize(im,&next,1/hs,"vscale",1/vs,"kernel",5,NULL));}
 int angle=orientation==6||orientation==5?1:orientation==3||orientation==4?2:orientation==8||orientation==7?3:0;
 if(angle){VipsImage *memory=vips_image_copy_memory(im);if(!memory)goto fail;g_object_unref(im);im=memory;STEP(vips_rot(im,&next,angle,NULL));}
 if(orientation==2||orientation==4||orientation==5||orientation==7){STEP(vips_flip(im,&next,0,NULL));}
 if(premul){STEP(vips_unpremultiply(im,&next,NULL));STEP(vips_cast(im,&next,band_format,NULL));}
 interp=vips_image_get_interpretation(im);
 if(interp==25||interp==26){STEP(vips_cast(im,&next,2,NULL));}
 if(interp!=22){STEP(vips_colourspace(im,&next,22,"source_space",interp,NULL));}
 int rc= thumbnail ? vips_webpsave_buffer(im,out,out_len,"keep",0,"Q",75,"lossless",0,"near_lossless",0,"smart_subsample",0,"smart_deblock",0,"preset",0,"effort",4,"min_size",0,"mixed",0,"exact",0,"alpha_q",100,NULL)
 :vips_pngsave_buffer(im,out,out_len,"keep",0,"compression",6,"filter",8,"interlace",0,"palette",0,"Q",100,"effort",7,"bitdepth",8,"dither",1.0,NULL);
 g_object_unref(im);return rc;
 fail:g_object_unref(im);return -1;
}


int he_meme_render(const void *data,size_t n,int page,int thumbnail,void **out,size_t *len,char **error){
 int rc=meme_render_image(data,n,page,thumbnail,out,len);*error=rc?vips_error_buffer_copy():NULL;
 if(rc&&*out){g_free(*out);*out=NULL;*len=0;}if(!rc)vips_error_clear();vips_thread_shutdown();return rc;
}

// chat-media.validateDisplayImage: metadata policy precedes sharp.stats().
int he_display_validate(const void *data,size_t n,he_metadata *m){
 int rc=-1;const char *fmt=format(data,n);VipsImage *im=NULL;
 if(fmt){im=open_image(data,n,fmt,1,1);if(im){
  m->width=vips_image_get_width(im);m->height=vips_image_get_height(im);
  m->pages=field(im,"n-pages",1);m->page_height=field(im,"page-height",m->height);strcpy(m->format,fmt);
  if(m->width>0&&m->height>0&&m->pages<=200&&(uint64_t)m->width*m->height*(uint64_t)m->pages<=40000000)rc=full_stats(im);
 }}
 if(im)g_object_unref(im);vips_error_clear();vips_thread_shutdown();return rc;
}
