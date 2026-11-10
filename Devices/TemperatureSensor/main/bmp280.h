#ifndef BMP280_H
#define BMP280_H

#include "esp_log.h"
#include "esp_err.h"

esp_err_t bmp280_init(void);
esp_err_t bmp280_read_temperature(float *temperature);

#endif