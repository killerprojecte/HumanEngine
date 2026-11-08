#ifndef HE_IMAGE_BRIDGE_H
#define HE_IMAGE_BRIDGE_H
#include <stddef.h>
typedef struct { int width,height,pages,page_height,orientation,alpha; char format[8]; } he_metadata;
int he_seal_library(int source_fd);
void *he_source_new(const void *,size_t);
void he_source_free(void *);
long he_live_inputs(void);
void he_trim_cache_for_test(void);
int he_init(const char *path);
const char *he_error(void);
int he_inspect(const void *,size_t,he_metadata *);
int he_render(const void *,size_t,int,int,int,void **,size_t *,char **);
void he_free(void *);
int he_meme_metadata(const void *,size_t,he_metadata *,char **);
int he_meme_render(const void *,size_t,int,int,void **,size_t *,char **);
#endif
